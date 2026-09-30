package web

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/juancer/nexo/internal/app"
	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
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

func TestDeletingANetworkKeepsTheRowWhenItsPasswordCannotBeRemoved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Built by hand instead of newEnv, to know the database path: a trigger on it
	// makes deleting a secret fail.
	path := filepath.Join(t.TempDir(), "n.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if _, err := secrets.GenerateKeyFile(keyPath); err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.LoadKey(keyPath)
	vault, err := secrets.New(key, st)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, system.Config{Hardware: "mock", DataDir: t.TempDir(), KeyPath: keyPath}, slog.New(slog.NewTextHandler(io.Discard, nil)), st, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	hash, _ := auth.HashPassword(testPassword)
	if _, err := st.CreateAdmin(ctx, "admin", hash, true, false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newServer(a, "test").handler(fstest.MapFS{"index.html": {Data: []byte("x")}}))
	t.Cleanup(srv.Close)
	e := &env{srv: srv, app: a, store: st}
	cookie := e.login(t)

	id, err := st.CreateWifiNetwork(ctx, store.WifiNetwork{SSID: "Home", SecretName: "wifi.1", Priority: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutSecret(ctx, "wifi.1", []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER no_secret_delete BEFORE DELETE ON secrets BEGIN SELECT RAISE(ABORT, 'locked'); END`); err != nil {
		t.Fatal(err)
	}

	resp := e.do(t, "DELETE", "/api/wifi/"+itoa(id), "", cookie, nil)
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if _, err := st.GetWifiNetwork(ctx, id); err != nil {
		t.Fatalf("the row was removed although its password was not: %v", err)
	}
}
