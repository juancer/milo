package wifi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidation(t *testing.T) {
	for _, ok := range []string{"Home", "Grandma's WiFi", strings.Repeat("x", 32), "Ünïcode"} {
		if err := ValidateSSID(ok); err != nil {
			t.Errorf("SSID %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 33), "bad\nname", "nul\x00", "tab\t"} {
		if ValidateSSID(bad) == nil {
			t.Errorf("SSID %q accepted", bad)
		}
	}
	for _, ok := range []string{"", "password1", strings.Repeat("a", 63), "with space & symbols !#$"} {
		if err := ValidatePassphrase(ok); err != nil {
			t.Errorf("passphrase %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"short", strings.Repeat("a", 64), "líneaéñe12", "new\nline123"} {
		if ValidatePassphrase(bad) == nil {
			t.Errorf("passphrase %q accepted", bad)
		}
	}
}

// fakeNM records every invocation, one argument per line, and answers the
// connection list from a file so tests can pre-populate existing profiles.
func fakeNM(t *testing.T, existing string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	list := filepath.Join(dir, "list.txt")
	os.WriteFile(list, []byte(existing), 0o600)
	bin = filepath.Join(dir, "nmcli")
	script := "#!/bin/sh\n{ echo '---'; for a in \"$@\"; do echo \"$a\"; done; } >> " + log + "\n" +
		"if [ \"$1\" = \"-t\" ] && [ \"$4\" = \"connection\" ]; then cat " + list + "; fi\n" +
		"if [ \"$1\" = \"-t\" ] && [ \"$4\" = \"device\" ]; then printf 'no:Other\\nyes:Home\\\\:5G\\n'; fi\n"
	os.WriteFile(bin, []byte(script), 0o755)
	return bin, log
}

func calls(t *testing.T, log string) [][]string {
	t.Helper()
	raw, _ := os.ReadFile(log)
	var out [][]string
	for _, block := range strings.Split(string(raw), "---\n") {
		if strings.TrimSpace(block) != "" {
			out = append(out, strings.Split(strings.TrimRight(block, "\n"), "\n"))
		}
	}
	return out
}

func TestApplyAddsUpdatesAndRemovesOnlyNexoProfiles(t *testing.T) {
	stale := profileName("Old Network")
	home := profileName("Home")
	bin, log := fakeNM(t, "Wired connection 1:802-3-ethernet\nmyown:802-11-wireless\n"+home+":802-11-wireless\n"+stale+":802-11-wireless\n")

	nm := NMCLI{Binary: bin}
	err := nm.Apply(context.Background(), []Network{
		{SSID: "Home", Password: "hunter2hunter2", Priority: 10},
		{SSID: `Evil "; rm -rf / $(id)`, Password: "", Priority: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	var modify, add, del [][]string
	for _, c := range got {
		switch {
		case len(c) > 1 && c[0] == "connection" && c[1] == "modify":
			modify = append(modify, c)
		case len(c) > 1 && c[0] == "connection" && c[1] == "add":
			add = append(add, c)
		case len(c) > 1 && c[0] == "connection" && c[1] == "delete":
			del = append(del, c)
		}
	}
	if len(modify) != 1 || modify[0][2] != home || !contains(modify[0], "wifi-sec.psk") || !contains(modify[0], "hunter2hunter2") || !contains(modify[0], "10") {
		t.Fatalf("modify = %v", modify)
	}
	// The awkward SSID is passed as one intact argument, never interpreted by a shell.
	if len(add) != 1 || !contains(add[0], `Evil "; rm -rf / $(id)`) || contains(add[0], "wifi-sec.psk") {
		t.Fatalf("add = %v", add)
	}
	// Only the stale Nexo profile is deleted; the user's own profiles are untouched.
	if len(del) != 1 || del[0][2] != stale {
		t.Fatalf("delete = %v", del)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestApplyErrorsDoNotContainPasswords(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "nmcli")
	os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = \"-t\" ]; then exit 0; fi\necho 'Error: connection failed' >&2\nexit 1\n"), 0o755)
	err := NMCLI{Binary: bin}.Apply(context.Background(), []Network{{SSID: "Home", Password: "SuperSecretPass1", Priority: 1}})
	if err == nil || strings.Contains(err.Error(), "SuperSecretPass1") || strings.Contains(err.Error(), "Home") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatus(t *testing.T) {
	bin, _ := fakeNM(t, "")
	st := NMCLI{Binary: bin}.Status(context.Background())
	if !st.Available || !st.Connected || st.SSID != "Home:5G" {
		t.Fatalf("status = %+v (escaped colon must be restored)", st)
	}
	missing := NMCLI{Binary: "/nonexistent/nmcli"}.Status(context.Background())
	if missing.Available || missing.Detail == "" {
		t.Fatalf("missing nmcli = %+v", missing)
	}
}

func TestMockPicksHighestPriority(t *testing.T) {
	m := NewMock()
	m.Apply(context.Background(), []Network{{SSID: "Hotspot", Priority: 1}, {SSID: "Home", Priority: 9}})
	if st := m.Status(context.Background()); st.SSID != "Home" {
		t.Fatalf("connected to %q", st.SSID)
	}
	if len(m.Applied()) != 2 {
		t.Fatal("applied networks not recorded")
	}
}

func TestApplyKeepLeavesTheProfileAlone(t *testing.T) {
	kept := profileName("Kept")
	bin, log := fakeNM(t, kept+":802-11-wireless\n")
	if err := (NMCLI{Binary: bin}).Apply(context.Background(), []Network{{SSID: "Kept", Keep: true}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls(t, log) {
		if len(c) > 1 && c[0] == "connection" {
			t.Fatalf("a kept profile was touched: %v", c)
		}
	}
}

func TestApplyRecreatesASecuredProfileThatBecomesOpen(t *testing.T) {
	home := profileName("Home")
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "nmcli")
	script := "#!/bin/sh\n{ echo '---'; for a in \"$@\"; do echo \"$a\"; done; } >> " + log + "\n" +
		"if [ \"$1\" = \"-t\" ]; then echo '" + home + ":802-11-wireless'; fi\n" +
		"if [ \"$1\" = \"-g\" ]; then echo wpa-psk; fi\n"
	os.WriteFile(bin, []byte(script), 0o755)
	if err := (NMCLI{Binary: bin}).Apply(context.Background(), []Network{{SSID: "Home", Priority: 3}}); err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, c := range calls(t, log) {
		if len(c) > 1 && c[0] == "connection" {
			seq = append(seq, c[1])
			if c[1] == "add" && contains(c, "wifi-sec.psk") {
				t.Fatalf("recreated profile still has a key: %v", c)
			}
		}
	}
	if strings.Join(seq, ",") != "delete,add" {
		t.Fatalf("connection calls = %v, want delete then add", seq)
	}
}
