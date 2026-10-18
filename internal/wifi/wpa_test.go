package wifi

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPSKKnownVector(t *testing.T) {
	// IEEE 802.11i-2004 annex H.4 test vector.
	got, err := psk("IEEE", "password")
	if err != nil || got != "f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e" {
		t.Fatalf("psk = %q, %v", got, err)
	}
}

// fakeSupplicant answers on a unix datagram socket named like wpa_supplicant's and records every
// command. Network 0 is the owner's own, network 3 belongs to Milo ("milo-old").
type fakeSupplicant struct {
	mu   sync.Mutex
	cmds []string
}

func startSupplicant(t *testing.T) (*fakeSupplicant, WPA) {
	t.Helper()
	dir := t.TempDir()
	srv, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: filepath.Join(dir, "wlan0"), Net: "unixgram"})
	if err != nil {
		t.Skip("cannot create a unix socket:", err)
	}
	t.Cleanup(func() { srv.Close() })
	f := &fakeSupplicant{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := srv.ReadFromUnix(buf)
			if err != nil {
				return
			}
			cmd := string(buf[:n])
			f.mu.Lock()
			f.cmds = append(f.cmds, cmd)
			f.mu.Unlock()
			reply := "OK\n"
			switch {
			case cmd == "LIST_NETWORKS":
				reply = "network id / ssid / bssid / flags\n0\tmine\tany\t[CURRENT]\n3\told\tany\t\n"
			case cmd == "GET_NETWORK 3 id_str":
				reply = "\"milo-old\"\n"
			case strings.HasPrefix(cmd, "GET_NETWORK"):
				reply = "FAIL\n"
			case cmd == "ADD_NETWORK":
				reply = "7\n"
			case cmd == "STATUS":
				reply = "wpa_state=COMPLETED\nssid=Home\n"
			}
			srv.WriteToUnix([]byte(reply), from)
		}
	}()
	return f, WPA{SocketDir: dir, ClientDir: t.TempDir()}
}

func (f *fakeSupplicant) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.cmds, "\n")
}

func TestWPAApplyOnlyTouchesMiloNetworks(t *testing.T) {
	f, w := startSupplicant(t)
	if err := w.Apply(context.Background(), []Network{{SSID: "Home", Password: "password1", Priority: 5}}); err != nil {
		t.Fatal(err)
	}
	all := f.all()
	for _, want := range []string{
		"ADD_NETWORK",
		"SET_NETWORK 7 id_str \"" + profileName("Home") + "\"",
		"SET_NETWORK 7 key_mgmt WPA-PSK",
		"SET_NETWORK 7 priority 5",
		"ENABLE_NETWORK 7",
		"REMOVE_NETWORK 3", // the stale Milo network
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing command %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "password1") {
		t.Error("the passphrase was sent instead of the derived key")
	}
	for _, bad := range []string{"REMOVE_NETWORK 0", "SET_NETWORK 0", "SAVE_CONFIG", "RECONFIGURE", "DISCONNECT"} {
		if strings.Contains(all, bad) {
			t.Errorf("touched what it does not own: %q", bad)
		}
	}
}

func TestWPAOpenNetworkAndStatus(t *testing.T) {
	f, w := startSupplicant(t)
	if err := w.Apply(context.Background(), []Network{{SSID: "Cafe", Priority: 1}}); err != nil {
		t.Fatal(err)
	}
	if all := f.all(); !strings.Contains(all, "SET_NETWORK 7 key_mgmt NONE") || strings.Contains(all, " psk ") {
		t.Errorf("open network not configured as open:\n%s", all)
	}
	if st := w.Status(context.Background()); !st.Available || !st.Connected || st.SSID != "Home" {
		t.Errorf("status = %+v", st)
	}
}

func TestWPAWithoutSocket(t *testing.T) {
	w := WPA{SocketDir: t.TempDir()}
	if st := w.Status(context.Background()); st.Available {
		t.Error("available without a control socket")
	}
	if err := w.Apply(context.Background(), nil); err == nil {
		t.Error("Apply succeeded without a control socket")
	}
}
