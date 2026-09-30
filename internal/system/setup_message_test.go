package system

import (
	"strings"
	"testing"
)

func TestInstalledMessageShowsOnlyTheAddress(t *testing.T) {
	fresh := InstalledMessage("192.168.1.169", false)
	for _, want := range []string{"http://192.168.1.169:8080", "finish setup"} {
		if !strings.Contains(fresh, want) {
			t.Errorf("fresh install message lacks %q:\n%s", want, fresh)
		}
	}
	for _, banned := range []string{"Password", "Username", "admin"} {
		if strings.Contains(fresh, banned) {
			t.Errorf("fresh install message mentions %q:\n%s", banned, fresh)
		}
	}
	if existing := InstalledMessage("192.168.1.169", true); !strings.Contains(existing, "admin-reset") {
		t.Errorf("existing install message lost the recovery hint:\n%s", existing)
	}
}
