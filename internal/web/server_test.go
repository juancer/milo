package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/juancer/nexo/internal/app"
	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/features"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
)

const testPassword = "a-long-test-password"

type fakeFeature struct{ started bool }

func (f *fakeFeature) Name() string                { return "telegram" }
func (f *fakeFeature) Start(context.Context) error { f.started = true; return nil }
func (f *fakeFeature) Stop(context.Context) error  { f.started = false; return nil }

type env struct {
	srv     *httptest.Server
	app     *app.App
	store   *store.Store
	feature *fakeFeature
}

func newEnv(t *testing.T, setPassword bool) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault, err := secrets.New(make([]byte, 32), st)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, system.Config{Hardware: "mock", DataDir: t.TempDir()}, log, st, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ff := &fakeFeature{}
	if err := a.Features.Register(ctx, ff); err != nil {
		t.Fatal(err)
	}
	if setPassword {
		hash, _ := auth.HashPassword(testPassword)
		st.SetSetting(ctx, settingPasswordHash, hash)
	}
	assets := fstest.MapFS{"index.html": {Data: []byte("<h1>Nexo</h1>")}}
	srv := httptest.NewServer(New(a, "test", assets))
	t.Cleanup(srv.Close)
	return &env{srv: srv, app: a, store: st, feature: ff}
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

func TestButtonsCRUDAndReload(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)

	var list []buttonDTO
	json.NewDecoder(e.do(t, "GET", "/api/buttons", "", cookie, nil).Body).Decode(&list)
	if len(list) != 4 || list[0].Color != "red" || list[0].GPIO != 17 {
		t.Fatalf("default buttons = %+v", list)
	}
	// Seeded actions belong to later phases, so they are flagged as unknown for now.
	if len(list[0].Unknown) == 0 {
		t.Fatalf("expected unknown action flag, got %+v", list[0])
	}

	body := `{"name":"Purple","color":"purple","gpio":5,"active_low":true,"enabled":true,
		"actions":{"short_press":{"action":"set_volume","parameters":{"change":10}}}}`
	resp := e.do(t, "POST", "/api/buttons", body, cookie, nil)
	var created buttonDTO
	json.NewDecoder(resp.Body).Decode(&created)
	if resp.StatusCode != 200 || created.ID == 0 || len(created.Unknown) != 0 {
		t.Fatalf("create = %d %+v", resp.StatusCode, created)
	}

	// The new mapping works immediately, without a restart.
	before := e.app.MockAudio.Status(context.Background()).Volume
	e.app.MockGPIO.Tap(5)
	deadline := time.Now().Add(2 * time.Second)
	for e.app.MockAudio.Status(context.Background()).Volume == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := e.app.MockAudio.Status(context.Background()).Volume; got != before+10 {
		t.Fatalf("volume = %d, want %d", got, before+10)
	}

	// Validation.
	for name, bad := range map[string]string{
		"gpio out of range": `{"name":"X","gpio":99,"enabled":true}`,
		"empty name":        `{"name":"","gpio":6,"enabled":true}`,
		"bad event":         `{"name":"X","gpio":6,"actions":{"triple":{"action":"a"}}}`,
		"bad action name":   `{"name":"X","gpio":6,"actions":{"short_press":{"action":"rm -rf /"}}}`,
	} {
		if resp := e.do(t, "POST", "/api/buttons", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := e.do(t, "POST", "/api/buttons", `{"name":"Dup","gpio":5,"enabled":true}`, cookie, nil); resp.StatusCode != 409 {
		t.Errorf("duplicate gpio = %d, want 409", resp.StatusCode)
	}

	if resp := e.do(t, "DELETE", "/api/buttons/"+itoa(created.ID), "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp := e.do(t, "DELETE", "/api/buttons/"+itoa(created.ID), "", cookie, nil); resp.StatusCode != 404 {
		t.Fatalf("delete twice = %d", resp.StatusCode)
	}
}

func TestButtonsRequireLogin(t *testing.T) {
	e := newEnv(t, true)
	for _, r := range [][2]string{{"GET", "/api/buttons"}, {"POST", "/api/buttons"}, {"GET", "/api/actions"}, {"GET", "/api/sim/audio"}} {
		if resp := e.do(t, r[0], r[1], "{}", nil, nil); resp.StatusCode != 401 {
			t.Errorf("%s %s = %d, want 401", r[0], r[1], resp.StatusCode)
		}
	}
}

func TestSimulatorButtonPressAndAudioLog(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	var buttons []buttonDTO
	json.NewDecoder(e.do(t, "GET", "/api/buttons", "", cookie, nil).Body).Decode(&buttons)
	// The default red button maps to "talk", which is not implemented yet, so
	// pressing it produces the error tone: the full path button -> engine -> audio.
	if resp := e.do(t, "POST", "/api/sim/buttons/"+itoa(buttons[0].ID)+"/press", `{"hold_ms":30}`, cookie, nil); resp.StatusCode != 202 {
		t.Fatalf("press = %d", resp.StatusCode)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var out struct {
			Played []struct{ Label string } `json:"played"`
		}
		json.NewDecoder(e.do(t, "GET", "/api/sim/audio", "", cookie, nil).Body).Decode(&out)
		if len(out.Played) > 0 && out.Played[0].Label == "error" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no error tone played: %+v", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp := e.do(t, "POST", "/api/sim/buttons/"+itoa(buttons[0].ID)+"/press", `{"hold_ms":1}`, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("bad hold = %d", resp.StatusCode)
	}
}

func TestActionsCatalogueListsDeviceActions(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	var list []actionDTO
	json.NewDecoder(e.do(t, "GET", "/api/actions", "", cookie, nil).Body).Decode(&list)
	names := map[string]bool{}
	for _, a := range list {
		names[a.Name] = true
	}
	if !names["set_volume"] || !names["get_device_status"] {
		t.Fatalf("actions = %+v", list)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
