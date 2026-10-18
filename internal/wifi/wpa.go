package wifi

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// WPA drives wpa_supplicant over its control socket, for systems that use dhcpcd and
// wpa_supplicant (Raspberry Pi OS Bullseye and earlier) instead of NetworkManager.
//
// It only ever touches networks whose id_str starts with "milo-". Networks already in
// wpa_supplicant.conf, the addressing in dhcpcd.conf and the current connection are left alone,
// and it never saves the configuration, so the file on disk is not rewritten: Milo applies its
// list again every time it starts. The service user must be in the group of the control socket,
// which is "netdev" on Raspberry Pi OS (the installer adds it).
//
// It speaks the control protocol itself instead of running wpa_cli: wpa_cli answers through a
// socket in /tmp, which Milo's private /tmp hides from wpa_supplicant.
type WPA struct {
	SocketDir string // default: /var/run/wpa_supplicant
	ClientDir string // where the reply socket lives; must be writable (default: the system temp dir)
}

func (w WPA) dir() string {
	if w.SocketDir != "" {
		return w.SocketDir
	}
	return "/var/run/wpa_supplicant"
}

// Iface returns the Wi-Fi interface wpa_supplicant controls, or "" when there is none.
func (w WPA) Iface() string {
	entries, err := os.ReadDir(w.dir())
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Type()&os.ModeSocket != 0 && !strings.HasPrefix(e.Name(), "p2p-dev-") {
			return e.Name()
		}
	}
	return ""
}

var wpaSeq atomic.Uint64

// run sends one command and returns the reply. The command may hold the derived Wi-Fi key, so
// it is never put in an error.
func (w WPA) run(ctx context.Context, iface string, args ...string) (string, error) {
	dir := w.ClientDir
	if dir == "" {
		dir = os.TempDir()
	}
	local := filepath.Join(dir, fmt.Sprintf(".wpa-ctrl-%d-%d", os.Getpid(), wpaSeq.Add(1)))
	defer os.Remove(local)
	conn, err := net.DialUnix("unixgram",
		&net.UnixAddr{Name: local, Net: "unixgram"},
		&net.UnixAddr{Name: filepath.Join(w.dir(), iface), Net: "unixgram"})
	if err != nil {
		return "", fmt.Errorf("wpa_supplicant %s failed: %w", args[0], err)
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	if _, err := conn.Write([]byte(strings.TrimSpace(strings.ToUpper(args[0]) + " " + strings.Join(args[1:], " ")))); err != nil {
		return "", fmt.Errorf("wpa_supplicant %s failed: %w", args[0], err)
	}
	buf := make([]byte, 16<<10)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("wpa_supplicant %s failed: %w", args[0], err)
	}
	return string(buf[:n]), nil
}

// do runs a command that prints OK on success.
func (w WPA) do(ctx context.Context, iface string, args ...string) error {
	out, err := w.run(ctx, iface, args...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "OK" {
		return fmt.Errorf("wpa_supplicant %s failed: %s", args[0], strings.TrimSpace(out))
	}
	return nil
}

// psk derives the 256-bit WPA key from a passphrase. Passing it instead of the passphrase keeps
// the password out of the process list and avoids quoting problems.
func psk(ssid, passphrase string) (string, error) {
	k, err := pbkdf2.Key(sha1.New, passphrase, []byte(ssid), 4096, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k), nil
}

// miloNetworks returns the ids of the networks Milo owns, by profile name.
func (w WPA) miloNetworks(ctx context.Context, iface string) (map[string]string, error) {
	out, err := w.run(ctx, iface, "list_networks")
	if err != nil {
		return nil, err
	}
	found := map[string]string{}
	for i, line := range strings.Split(out, "\n") {
		id, _, _ := strings.Cut(line, "\t")
		if i == 0 || id == "" { // header
			continue
		}
		name, err := w.run(ctx, iface, "get_network", id, "id_str")
		if err != nil {
			return nil, err
		}
		if name = strings.Trim(strings.TrimSpace(name), `"`); strings.HasPrefix(name, profilePrefix) {
			found[name] = id
		}
	}
	return found, nil
}

// Apply implements Manager.
func (w WPA) Apply(ctx context.Context, networks []Network) error {
	iface := w.Iface()
	if iface == "" {
		return fmt.Errorf("wpa_supplicant has no control socket in %s", w.dir())
	}
	existing, err := w.miloNetworks(ctx, iface)
	if err != nil {
		return err
	}
	var firstErr error
	fail := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	keep := map[string]bool{}
	for _, nw := range networks {
		name := profileName(nw.SSID)
		keep[name] = true
		if nw.Keep {
			continue
		}
		id, ok := existing[name]
		if !ok {
			out, err := w.run(ctx, iface, "add_network")
			if err != nil {
				fail(err)
				continue
			}
			id = strings.TrimSpace(out)
			if _, err := strconv.Atoi(id); err != nil {
				fail(fmt.Errorf("wpa_supplicant add_network failed: %s", id))
				continue
			}
		}
		fail(w.configure(ctx, iface, id, name, nw))
	}
	for name, id := range existing {
		if !keep[name] {
			fail(w.do(ctx, iface, "remove_network", id))
		}
	}
	return firstErr
}

// configure sets every field of one network, so an edit from secured to open leaves no old key.
func (w WPA) configure(ctx context.Context, iface, id, name string, nw Network) error {
	set := func(field, value string) error { return w.do(ctx, iface, "set_network", id, field, value) }
	steps := [][2]string{
		{"id_str", `"` + name + `"`},
		{"ssid", hex.EncodeToString([]byte(nw.SSID))},
		{"priority", strconv.Itoa(nw.Priority)},
	}
	if nw.Password == "" {
		steps = append(steps, [2]string{"key_mgmt", "NONE"})
	} else {
		key, err := psk(nw.SSID, nw.Password)
		if err != nil {
			return err
		}
		steps = append(steps, [2]string{"key_mgmt", "WPA-PSK"}, [2]string{"psk", key})
	}
	for _, s := range steps {
		if err := set(s[0], s[1]); err != nil {
			return err
		}
	}
	return w.do(ctx, iface, "enable_network", id)
}

// Status implements Manager.
func (w WPA) Status(ctx context.Context) Status {
	iface := w.Iface()
	if iface == "" {
		return Status{Detail: "wpa_supplicant is not running; configure Wi-Fi with your operating system"}
	}
	out, err := w.run(ctx, iface, "status")
	if err != nil {
		return Status{Detail: "cannot query Wi-Fi: " + err.Error()}
	}
	st := Status{Available: true}
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "wpa_state":
			st.Connected = v == "COMPLETED"
		case "ssid":
			st.SSID = v
		}
	}
	if !st.Connected {
		st.SSID = ""
	}
	return st
}

// Detect picks the Wi-Fi backend for this system: NetworkManager when it is running, otherwise
// wpa_supplicant when it has a control socket, otherwise NetworkManager (whose Status explains
// that Wi-Fi cannot be managed here).
func Detect(clientDir string) Manager {
	if p, err := exec.LookPath("nmcli"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, p, "-t", "-f", "RUNNING", "general").Output(); err == nil && strings.TrimSpace(string(out)) == "running" {
			return NMCLI{}
		}
	}
	if w := (WPA{ClientDir: clientDir}); w.Iface() != "" {
		return w
	}
	return NMCLI{}
}
