package web

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
)

func TestWifiRowIsRemovedWhenThePasswordCannotBeStored(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	// A vault on a closed database makes Put fail.
	dead, err := store.Open(ctx, filepath.Join(t.TempDir(), "dead.db"))
	if err != nil {
		t.Fatal(err)
	}
	vault, _ := secrets.New(make([]byte, 32), dead)
	dead.Close()
	e.app.Vault = vault

	resp := e.do(t, "POST", "/api/wifi", `{"ssid":"Home","password":"home-wifi-password","priority":5,"enabled":true}`, cookie, nil)
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	nets, _ := e.store.ListWifiNetworks(ctx)
	if len(nets) != 0 {
		t.Fatalf("a row without a password was left behind: %+v", nets)
	}
}
