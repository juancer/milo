package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/juancer/nexo/internal/audit"
	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/features"
	"github.com/juancer/nexo/internal/store"
)

const testPassword = "a-long-test-password"

type fakeFeature struct{ started bool }

func (f *fakeFeature) Name() string                { return "telegram" }
func (f *fakeFeature) Start(context.Context) error { f.started = true; return nil }
func (f *fakeFeature) Stop(context.Context) error  { f.started = false; return nil }

type env struct {
	srv     *httptest.Server
	store   *store.Store
	feature *fakeFeature
}

func newEnv(t *testing.T, setPassword bool) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fm := features.NewManager(st, log)
	ff := &fakeFeature{}
	if err := fm.Register(ctx, ff); err != nil {
		t.Fatal(err)
	}
	if setPassword {
		hash, _ := auth.HashPassword(testPassword)
		st.SetSetting(ctx, settingPasswordHash, hash)
	}
	assets := fstest.MapFS{"index.html": {Data: []byte("<h1>Nexo</h1>")}}
	h := New(Deps{Store: st, Features: fm, Audit: audit.New(st, log), Log: log, Version: "test", Hardware: "mock"}, assets)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{srv: srv, store: st, feature: ff}
}

func (e *env) do(t *testing.T, method, path, body string, cookie *http.Cookie, headers map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set(clientHeader, clientHeaderValue)
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (e *env) login(t *testing.T) *http.Cookie {
	t.Helper()
	resp := e.do(t, "POST", "/api/login", `{"password":"`+testPassword+`"}`, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("session cookie not hardened: %+v", c)
			}
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestHealthIsPublic(t *testing.T) {
	e := newEnv(t, false)
	resp := e.do(t, "GET", "/health", "", nil, nil)
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != 200 || body["status"] != "ok" {
		t.Fatalf("health = %d %v", resp.StatusCode, body)
	}
}

func TestServesEmbeddedUIWithSecurityHeaders(t *testing.T) {
	e := newEnv(t, false)
	resp := e.do(t, "GET", "/", "", nil, nil)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "Nexo") {
		t.Fatalf("index not served: %s", b)
	}
	if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("security headers missing")
	}
}

func TestProtectedEndpointsRequireLogin(t *testing.T) {
	e := newEnv(t, true)
	for _, p := range []string{"/api/status", "/api/features", "/api/audit"} {
		if resp := e.do(t, "GET", p, "", nil, nil); resp.StatusCode != 401 {
			t.Errorf("GET %s without login = %d, want 401", p, resp.StatusCode)
		}
	}
	bogus := &http.Cookie{Name: cookieName, Value: "not-a-session"}
	if resp := e.do(t, "GET", "/api/status", "", bogus, nil); resp.StatusCode != 401 {
		t.Errorf("bogus cookie = %d", resp.StatusCode)
	}
}

func TestSetupRequiredUntilPasswordSet(t *testing.T) {
	e := newEnv(t, false)
	var info map[string]bool
	json.NewDecoder(e.do(t, "GET", "/api/session", "", nil, nil).Body).Decode(&info)
	if !info["setup_required"] || info["authenticated"] {
		t.Fatalf("session info = %v", info)
	}
	if resp := e.do(t, "POST", "/api/login", `{"password":"anything at all"}`, nil, nil); resp.StatusCode != 409 {
		t.Fatalf("login before setup = %d, want 409", resp.StatusCode)
	}
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t, true)
	if resp := e.do(t, "POST", "/api/login", `{"password":"wrong password!"}`, nil, nil); resp.StatusCode != 401 {
		t.Fatalf("wrong password = %d", resp.StatusCode)
	}
	cookie := e.login(t)

	resp := e.do(t, "GET", "/api/status", "", cookie, nil)
	var st map[string]any
	json.NewDecoder(resp.Body).Decode(&st)
	if resp.StatusCode != 200 || st["hardware"] != "mock" || st["version"] != "test" {
		t.Fatalf("status = %d %v", resp.StatusCode, st)
	}

	e.do(t, "POST", "/api/logout", "", cookie, nil)
	if resp := e.do(t, "GET", "/api/status", "", cookie, nil); resp.StatusCode != 401 {
		t.Fatalf("session still valid after logout: %d", resp.StatusCode)
	}

	entries, _ := e.store.ListAudit(context.Background(), 10)
	actions := []string{}
	for _, en := range entries {
		actions = append(actions, en.Action)
	}
	joined := strings.Join(actions, ",")
	if !strings.Contains(joined, "admin.login_failed") || !strings.Contains(joined, "admin.login") {
		t.Fatalf("audit trail = %s", joined)
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	e := newEnv(t, true)
	for i := 0; i < 5; i++ {
		e.do(t, "POST", "/api/login", `{"password":"wrong password!"}`, nil, nil)
	}
	// Even the right password is refused while throttled.
	if resp := e.do(t, "POST", "/api/login", `{"password":"`+testPassword+`"}`, nil, nil); resp.StatusCode != 429 {
		t.Fatalf("throttled login = %d, want 429", resp.StatusCode)
	}
}

func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	newHash, _ := auth.HashPassword("another-long-password")
	e.store.SetSetting(context.Background(), settingPasswordHash, newHash)
	if resp := e.do(t, "GET", "/api/status", "", cookie, nil); resp.StatusCode != 401 {
		t.Fatalf("old session survived password change: %d", resp.StatusCode)
	}
}

func TestCSRFProtection(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)

	// Missing custom header.
	req, _ := http.NewRequest("PUT", e.srv.URL+"/api/features/telegram", strings.NewReader(`{"enabled":true}`))
	req.AddCookie(cookie)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("no client header = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Cross-origin.
	resp = e.do(t, "PUT", "/api/features/telegram", `{"enabled":true}`, cookie, map[string]string{"Origin": "http://evil.example"})
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin = %d, want 403", resp.StatusCode)
	}
	if e.feature.started {
		t.Fatal("feature toggled by a forged request")
	}
}

func TestFeatureToggle(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)

	if resp := e.do(t, "PUT", "/api/features/telegram", `{"enabled":true}`, cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("enable = %d", resp.StatusCode)
	}
	if !e.feature.started {
		t.Fatal("feature not started")
	}
	var list []features.Status
	json.NewDecoder(e.do(t, "GET", "/api/features", "", cookie, nil).Body).Decode(&list)
	if len(list) != 1 || !list[0].Enabled || !list[0].Running {
		t.Fatalf("list = %+v", list)
	}
	if resp := e.do(t, "PUT", "/api/features/ghost", `{"enabled":true}`, cookie, nil); resp.StatusCode != 404 {
		t.Fatalf("unknown feature = %d", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/api/features/telegram", `{}`, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("missing enabled = %d", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/api/features/telegram", `{"enabled":true,"extra":1}`, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("unknown field = %d", resp.StatusCode)
	}
	e.do(t, "PUT", "/api/features/telegram", `{"enabled":false}`, cookie, nil)
	if e.feature.started {
		t.Fatal("feature not stopped")
	}
}
