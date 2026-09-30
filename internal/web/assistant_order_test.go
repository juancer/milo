package web

import (
	"context"
	"database/sql"
	"encoding/json"
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

// newEnvAt is newEnv with the database file at a known path, so a test can add a trigger to it.
func newEnvAt(t *testing.T, dbPath string) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, dbPath)
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
	a, err := app.New(ctx, system.Config{Hardware: "mock", DataDir: t.TempDir(), KeyPath: keyPath},
		slog.New(slog.NewTextHandler(io.Discard, nil)), st, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	hash, _ := auth.HashPassword(testPassword)
	if _, err := st.CreateAdmin(ctx, "admin", hash, true, false); err != nil {
		t.Fatal(err)
	}
	impl := newServer(a, "test")
	srv := httptest.NewServer(impl.handler(fstest.MapFS{"index.html": {Data: []byte("<h1>Nexo</h1>")}}))
	t.Cleanup(srv.Close)
	return &env{srv: srv, app: a, store: st, impl: impl}
}

func TestOldKeyIsDeletedBeforeTheNewAddressIsSaved(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "n.db")
	e := newEnvAt(t, dbPath)
	cookie := e.login(t)
	d := decode[struct {
		Settings assistantDTO `json:"settings"`
	}](t, e.do(t, "GET", "/api/assistant", "", cookie, nil)).Settings
	if resp := e.do(t, "PUT", "/api/assistant/keys", `{"llm_api_key":"sk-or-v1-SOME-LONG-KEY"}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("keys = %d", resp.StatusCode)
	}
	// Saving settings fails while the AI key is still stored: that is the window in which the new
	// address would be paired with the old key.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER key_still_there BEFORE UPDATE ON gpt_settings
		WHEN (SELECT COUNT(*) FROM secrets WHERE name = 'assistant.llm_api_key') > 0
		BEGIN SELECT RAISE(ABORT, 'old key still stored'); END`); err != nil {
		t.Fatal(err)
	}
	d.LLMBaseURL = "https://other.example/v1"
	b, _ := json.Marshal(d)
	if resp := e.do(t, "PUT", "/api/assistant", string(b), cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("PUT = %d: the settings were saved while the old key was still stored", resp.StatusCode)
	}
}

func TestUnreadableKeyIsStillDeletedWhenTheAddressChanges(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	d := decode[struct {
		Settings assistantDTO `json:"settings"`
	}](t, e.do(t, "GET", "/api/assistant", "", cookie, nil)).Settings
	e.store.PutSecret(context.Background(), "assistant.llm_api_key", []byte("not a sealed value, cannot be decrypted at all!!"))
	d.LLMBaseURL = "https://other.example/v1"
	b, _ := json.Marshal(d)
	if resp := e.do(t, "PUT", "/api/assistant", string(b), cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	if _, err := e.store.GetSecret(context.Background(), "assistant.llm_api_key"); err == nil {
		t.Fatal("an unreadable key stayed paired with the new address")
	}
}
