// Package wifi manages the Wi-Fi networks the device may join. Networks live in
// SQLite (passwords encrypted); this package applies them to NetworkManager,
// which then connects to the best available one by priority.
package wifi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Network is a network to configure. An empty Password means an open network.
type Network struct {
	SSID     string
	Password string
	Priority int // higher is preferred
	// Keep leaves an existing profile untouched (its password was unavailable
	// this time) instead of deleting it; nothing else in the struct is used.
	Keep bool
}

// Status describes the current connection.
type Status struct {
	Available bool   `json:"available"` // NetworkManager can be controlled
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Manager applies networks and reports status.
type Manager interface {
	// Apply makes the set of Nexo-managed networks equal to the given list:
	// adding, updating and removing profiles. Networks not created by Nexo are
	// never touched.
	Apply(ctx context.Context, networks []Network) error
	Status(ctx context.Context) Status
}

// ValidateSSID checks an SSID (1-32 bytes, no control characters).
func ValidateSSID(s string) error {
	if len(s) < 1 || len(s) > 32 {
		return errors.New("the network name must be 1-32 characters")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return errors.New("the network name contains invalid characters")
		}
	}
	return nil
}

// ValidatePassphrase checks a WPA passphrase: empty (open network) or 8-63
// printable ASCII characters.
func ValidatePassphrase(p string) error {
	if p == "" {
		return nil
	}
	if len(p) < 8 || len(p) > 63 {
		return errors.New("the password must be 8-63 characters (or empty for an open network)")
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] > 0x7e {
			return errors.New("the password may only contain printable ASCII characters")
		}
	}
	return nil
}

// profilePrefix marks connection profiles owned by Nexo.
const profilePrefix = "nexo-"

func profileName(ssid string) string {
	sum := sha256.Sum256([]byte(ssid))
	return profilePrefix + hex.EncodeToString(sum[:])[:12]
}

// ---------------------------------------------------------------- NetworkManager

// NMCLI drives NetworkManager through nmcli with argument lists (never a
// shell). On Raspberry Pi OS (Bookworm and later) NetworkManager is the default.
// The service user must be allowed to manage connections, which is the case
// for members of the "netdev" group (the installer adds it).
//
// Note: nmcli receives the Wi-Fi password as a command-line argument, so it is
// briefly visible to other local users of the device through the process list.
// nmcli has no stdin option for it that is documented well enough to rely on;
// see docs/wifi.md.
type NMCLI struct{ Binary string }

func (n NMCLI) bin() string {
	if n.Binary != "" {
		return n.Binary
	}
	return "nmcli"
}

func (n NMCLI) run(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, n.bin(), args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		// Never include args: they may contain a password.
		return "", fmt.Errorf("nmcli %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// secured reports whether the profile has a Wi-Fi security setting. If that
// cannot be read it assumes it does, which only costs a recreated profile.
func (n NMCLI) secured(ctx context.Context, name string) bool {
	out, err := n.run(ctx, "-g", "802-11-wireless-security.key-mgmt", "connection", "show", name)
	return err != nil || strings.TrimSpace(out) != ""
}

// Apply implements Manager.
func (n NMCLI) Apply(ctx context.Context, networks []Network) error {
	out, err := n.run(ctx, "-t", "-f", "NAME,TYPE", "connection", "show")
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		name, typ, ok := strings.Cut(strings.TrimSpace(line), ":")
		// Newer nmcli prints "wifi" in the TYPE column, older ones "802-11-wireless".
		if ok && strings.HasPrefix(name, profilePrefix) && (typ == "wifi" || strings.Contains(typ, "wireless")) {
			existing[name] = true
		}
	}
	var firstErr error
	keep := map[string]bool{}
	for _, nw := range networks {
		name := profileName(nw.SSID)
		keep[name] = true
		if nw.Keep {
			continue
		}
		if existing[name] && nw.Password == "" && n.secured(ctx, name) {
			// Editing a secured profile into an open one would leave its old
			// key-mgmt and PSK behind, so start again from a clean profile.
			if _, err := n.run(ctx, "connection", "delete", name); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			existing[name] = false
		}
		prio := strconv.Itoa(nw.Priority)
		var args []string
		if existing[name] {
			args = []string{"connection", "modify", name, "802-11-wireless.ssid", nw.SSID,
				"connection.autoconnect", "yes", "connection.autoconnect-priority", prio}
		} else {
			args = []string{"connection", "add", "type", "wifi", "con-name", name, "ifname", "*", "ssid", nw.SSID,
				"connection.autoconnect", "yes", "connection.autoconnect-priority", prio}
		}
		if nw.Password != "" {
			args = append(args, "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", nw.Password)
		}
		if _, err := n.run(ctx, args...); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for name := range existing {
		if !keep[name] {
			if _, err := n.run(ctx, "connection", "delete", name); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Status implements Manager.
func (n NMCLI) Status(ctx context.Context) Status {
	if _, err := exec.LookPath(n.bin()); err != nil {
		return Status{Detail: "NetworkManager (nmcli) is not installed; configure Wi-Fi with your operating system"}
	}
	out, err := n.run(ctx, "-t", "-f", "ACTIVE,SSID", "device", "wifi")
	if err != nil {
		return Status{Detail: "cannot query Wi-Fi: " + err.Error()}
	}
	st := Status{Available: true}
	for _, line := range strings.Split(out, "\n") {
		if active, ssid, ok := strings.Cut(line, ":"); ok && active == "yes" {
			st.Connected, st.SSID = true, unescape(ssid)
			break
		}
	}
	return st
}

// unescape reverses nmcli's terse-mode escaping of ':' and '\'.
func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ---------------------------------------------------------------- mock

// Mock is an in-memory Manager for tests and development.
type Mock struct {
	mu        sync.Mutex
	applied   []Network
	connected string
	failNext  error
}

// NewMock returns a Mock that reports being connected to nothing.
func NewMock() *Mock { return &Mock{} }

// Apply implements Manager. The mock "connects" to the highest-priority network.
func (m *Mock) Apply(_ context.Context, networks []Network) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext != nil {
		err := m.failNext
		m.failNext = nil
		return err
	}
	m.applied = append([]Network(nil), networks...)
	m.connected = ""
	best := -1
	for _, n := range networks {
		if !n.Keep && n.Priority > best {
			best, m.connected = n.Priority, n.SSID
		}
	}
	return nil
}

// Status implements Manager.
func (m *Mock) Status(context.Context) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Available: true, Connected: m.connected != "", SSID: m.connected, Detail: "simulated"}
}

// Applied returns the networks last applied.
func (m *Mock) Applied() []Network {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Network(nil), m.applied...)
}

// FailNext makes the next Apply fail.
func (m *Mock) FailNext(err error) {
	m.mu.Lock()
	m.failNext = err
	m.mu.Unlock()
}
