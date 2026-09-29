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

func (f *fakeFeature) Name() string                { return "demo" }
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
	req, _ := http.NewRequest("PUT", e.srv.URL+"/api/features/demo", strings.NewReader(`{"enabled":true}`))
	req.AddCookie(cookie)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("no client header = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Cross-origin.
	resp = e.do(t, "PUT", "/api/features/demo", `{"enabled":true}`, cookie, map[string]string{"Origin": "http://evil.example"})
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

	if resp := e.do(t, "PUT", "/api/features/demo", `{"enabled":true}`, cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("enable = %d", resp.StatusCode)
	}
	if !e.feature.started {
		t.Fatal("feature not started")
	}
	var list []features.Status
	json.NewDecoder(e.do(t, "GET", "/api/features", "", cookie, nil).Body).Decode(&list)
	var demo features.Status
	for _, f := range list {
		if f.Name == "demo" {
			demo = f
		}
	}
	if !demo.Enabled || !demo.Running {
		t.Fatalf("list = %+v", list)
	}
	if resp := e.do(t, "PUT", "/api/features/ghost", `{"enabled":true}`, cookie, nil); resp.StatusCode != 404 {
		t.Fatalf("unknown feature = %d", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/api/features/demo", `{}`, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("missing enabled = %d", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/api/features/demo", `{"enabled":true,"extra":1}`, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("unknown field = %d", resp.StatusCode)
	}
	e.do(t, "PUT", "/api/features/demo", `{"enabled":false}`, cookie, nil)
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
	if len(list[0].Unknown) != 0 {
		t.Fatalf("default 'talk' mapping should be a known action: %+v", list[0])
	}
	odd := `{"name":"Odd","gpio":6,"enabled":true,"actions":{"short_press":{"action":"time_travel"}}}`
	var oddDTO buttonDTO
	json.NewDecoder(e.do(t, "POST", "/api/buttons", odd, cookie, nil).Body).Decode(&oddDTO)
	if len(oddDTO.Unknown) != 1 || oddDTO.Unknown[0] != "time_travel" {
		t.Fatalf("unregistered action must be flagged: %+v", oddDTO)
	}
	e.do(t, "DELETE", "/api/buttons/"+itoa(oddDTO.ID), "", cookie, nil)

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

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestInvitationAPI(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	e.store.SetSetting(context.Background(), "telegram.bot_username", "family_bot")

	resp := e.do(t, "POST", "/api/invitations", `{"role":"FAMILY","ttl_hours":2,"note":"grandma"}`, cookie, nil)
	inv := decode[map[string]any](t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create = %d %v", resp.StatusCode, inv)
	}
	token, _ := inv["token"].(string)
	if len(token) < 40 || inv["link"] != "https://t.me/family_bot?start="+token {
		t.Fatalf("invitation = %v", inv)
	}
	// The raw token is not stored, only its hash, and it is not in the list.
	list := e.do(t, "GET", "/api/invitations", "", cookie, nil)
	raw, _ := io.ReadAll(list.Body)
	if strings.Contains(string(raw), token) || !strings.Contains(string(raw), `"status":"active"`) {
		t.Fatalf("list leaks token or lacks status: %s", raw)
	}
	entries, _ := e.store.ListAudit(context.Background(), 20)
	for _, en := range entries {
		if strings.Contains(en.Details, token) || strings.Contains(en.Target, token) {
			t.Fatal("invitation token written to the audit log")
		}
	}

	for name, bad := range map[string]string{
		"owner":     `{"role":"OWNER"}`,
		"nonsense":  `{"role":"ROOT"}`,
		"huge ttl":  `{"role":"FAMILY","ttl_hours":9999}`,
		"long note": `{"role":"FAMILY","note":"` + strings.Repeat("x", 100) + `"}`,
	} {
		if resp := e.do(t, "POST", "/api/invitations", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400 (invitations must never grant OWNER)", name, resp.StatusCode)
		}
	}

	id := itoa(int64(inv["id"].(float64)))
	if resp := e.do(t, "DELETE", "/api/invitations/"+id, "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	if resp := e.do(t, "DELETE", "/api/invitations/"+id, "", cookie, nil); resp.StatusCode != 404 {
		t.Fatalf("revoke twice = %d", resp.StatusCode)
	}
}

func TestAuthorizationApprovalCreatesUserAndChat(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	pid, _ := e.store.CreatePending(ctx, store.PendingAuthorization{TelegramID: 77, Username: "mami", FirstName: "Mami", Role: "PARENT"})
	e.store.CreatePending(ctx, store.PendingAuthorization{TelegramID: 78, FirstName: "Nope", Role: "FAMILY"})

	pending := decode[[]map[string]any](t, e.do(t, "GET", "/api/authorizations", "", cookie, nil))
	if len(pending) != 2 || pending[0]["username"] != "mami" {
		t.Fatalf("pending = %v", pending)
	}
	if resp := e.do(t, "POST", "/api/authorizations/"+itoa(pid)+"/approve", `{"display_name":"Mum","create_chat":true}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("approve = %d", resp.StatusCode)
	}
	users := decode[[]userDTO](t, e.do(t, "GET", "/api/users", "", cookie, nil))
	if len(users) != 1 || users[0].DisplayName != "Mum" || users[0].Role != "PARENT" || !users[0].Effective["emergency_alert"] || users[0].Effective["manage_configuration"] {
		t.Fatalf("users = %+v", users)
	}
	chats := decode[[]chatDTO](t, e.do(t, "GET", "/api/chats", "", cookie, nil))
	if len(chats) != 1 || chats[0].Name != "Mum" || chats[0].TelegramChatID != 77 || !chats[0].Favorite {
		t.Fatalf("chats = %+v", chats)
	}
	if resp := e.do(t, "POST", "/api/authorizations/"+itoa(pid)+"/approve", `{}`, cookie, nil); resp.StatusCode != 404 {
		t.Fatalf("approve twice = %d", resp.StatusCode)
	}
	pend2 := decode[[]map[string]any](t, e.do(t, "GET", "/api/authorizations", "", cookie, nil))
	if resp := e.do(t, "POST", "/api/authorizations/"+itoa(int64(pend2[0]["id"].(float64)))+"/reject", "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("reject = %d", resp.StatusCode)
	}
	if list, _ := e.store.ListPending(ctx); len(list) != 0 {
		t.Fatal("still pending after reject")
	}
}

func TestUserPermissionOverridesAndEmergencyPermission(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	uid, _ := e.store.CreateTelegramUser(context.Background(), "Grandma", "FAMILY", 5, "gran")

	body := `{"display_name":"Grandma","role":"FAMILY","active":true,"permissions":{"emergency_alert":true,"send_message":false}}`
	u := decode[userDTO](t, e.do(t, "PUT", "/api/users/"+itoa(uid), body, cookie, nil))
	if !u.Effective["emergency_alert"] || u.Effective["send_message"] || !u.Effective["receive_message"] {
		t.Fatalf("effective = %v", u.Effective)
	}
	// null clears the override so the role default applies again.
	u = decode[userDTO](t, e.do(t, "PUT", "/api/users/"+itoa(uid),
		`{"display_name":"Grandma","role":"FAMILY","active":true,"permissions":{"emergency_alert":null,"send_message":null}}`, cookie, nil))
	if u.Effective["emergency_alert"] || !u.Effective["send_message"] || len(u.Overrides) != 0 {
		t.Fatalf("after clearing: %+v", u)
	}
	for name, bad := range map[string]string{
		"unknown permission": `{"display_name":"G","role":"FAMILY","active":true,"permissions":{"shell":true}}`,
		"bad role":           `{"display_name":"G","role":"ROOT","active":true}`,
		"empty name":         `{"display_name":"","role":"FAMILY","active":true}`,
	} {
		if resp := e.do(t, "PUT", "/api/users/"+itoa(uid), bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d", name, resp.StatusCode)
		}
	}
	if resp := e.do(t, "PUT", "/api/users/999", `{"display_name":"x","role":"FAMILY","active":true}`, cookie, nil); resp.StatusCode != 404 {
		t.Errorf("unknown user = %d", resp.StatusCode)
	}
	entries, _ := e.store.ListAudit(context.Background(), 20)
	found := false
	for _, en := range entries {
		found = found || en.Action == "user.updated"
	}
	if !found {
		t.Fatal("permission change was not audited")
	}
}

func TestChatsAPIValidation(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	uid, _ := e.store.CreateTelegramUser(context.Background(), "Dad", "PARENT", 42, "dad")

	ok := `{"name":"Dad","kind":"user","user_id":` + itoa(uid) + `,"favorite":true,"position":1,"enabled":true}`
	c := decode[chatDTO](t, e.do(t, "POST", "/api/chats", ok, cookie, nil))
	if c.ID == 0 || c.TelegramChatID != 42 {
		t.Fatalf("chat = %+v (destination must come from the authorized user, not the request)", c)
	}
	// A client cannot point a "user" chat at an arbitrary Telegram id.
	spoof := `{"name":"Spoof","kind":"user","user_id":` + itoa(uid) + `,"telegram_chat_id":999999,"enabled":true}`
	s := decode[chatDTO](t, e.do(t, "POST", "/api/chats", spoof, cookie, nil))
	if s.TelegramChatID != 42 {
		t.Fatalf("spoofed destination accepted: %+v", s)
	}
	for name, bad := range map[string]string{
		"unknown user": `{"name":"X","kind":"user","user_id":999,"enabled":true}`,
		"group no id":  `{"name":"G","kind":"group","enabled":true}`,
		"bad kind":     `{"name":"G","kind":"channel","enabled":true}`,
		"empty name":   `{"name":"","kind":"group","telegram_chat_id":-5}`,
	} {
		if resp := e.do(t, "POST", "/api/chats", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := e.do(t, "POST", "/api/chats", ok, cookie, nil); resp.StatusCode != 409 {
		t.Errorf("duplicate name = %d, want 409", resp.StatusCode)
	}
	if resp := e.do(t, "POST", "/api/chats", `{"name":"Family","kind":"group","telegram_chat_id":-1001,"favorite":true,"enabled":true}`, cookie, nil); resp.StatusCode != 200 {
		t.Errorf("group = %d", resp.StatusCode)
	}
}

func TestTelegramTokenIsValidatedStoredEncryptedAndNeverEchoed(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	const tok = "123456:ABCdefGhIJKlmnoPQRstuvWXyz-0123456789"

	if resp := e.do(t, "PUT", "/api/telegram/token", `{"token":"not-a-token"}`, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("bad token = %d", resp.StatusCode)
	}
	resp := e.do(t, "PUT", "/api/telegram/token", `{"token":"`+tok+`"}`, cookie, nil)
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.Contains(string(raw), tok) || !strings.Contains(string(raw), `"configured":true`) {
		t.Fatalf("set token = %d %s", resp.StatusCode, raw)
	}
	stored, _ := e.store.GetSecret(context.Background(), "telegram.bot_token")
	if strings.Contains(string(stored), "ABCdef") {
		t.Fatal("token stored in plaintext")
	}
	entries, _ := e.store.ListAudit(context.Background(), 20)
	for _, en := range entries {
		if strings.Contains(en.Details+en.Target, "ABCdef") {
			t.Fatal("token in audit log")
		}
	}
	if resp := e.do(t, "DELETE", "/api/telegram/token", "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("delete token = %d", resp.StatusCode)
	}
	raw2, _ := io.ReadAll(e.do(t, "GET", "/api/telegram", "", cookie, nil).Body)
	if !strings.Contains(string(raw2), `"configured":false`) {
		t.Fatalf("status after delete: %s", raw2)
	}
}

func TestMessagesListHasNoContent(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	uid, _ := e.store.CreateTelegramUser(context.Background(), "Dad", "PARENT", 42, "dad")
	e.store.InsertMessage(context.Background(), store.Message{Direction: "in", Kind: "text", Body: "TOP-SECRET-BODY", UserID: uid})
	raw, _ := io.ReadAll(e.do(t, "GET", "/api/messages", "", cookie, nil).Body)
	if strings.Contains(string(raw), "TOP-SECRET") || !strings.Contains(string(raw), `"person":"Dad"`) {
		t.Fatalf("messages = %s", raw)
	}
}

func TestNotificationSettingsAPI(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	cfg := decode[map[string]any](t, e.do(t, "GET", "/api/settings/notifications", "", cookie, nil))
	if cfg["new_message_sound_enabled"] != true || cfg["volume"] != float64(100) {
		t.Fatalf("defaults = %v", cfg)
	}
	put := `{"new_message_sound_enabled":false,"emergency_sound_enabled":true,"notify_pending_after_schedule":false,"volume":40}`
	cfg = decode[map[string]any](t, e.do(t, "PUT", "/api/settings/notifications", put, cookie, nil))
	if cfg["new_message_sound_enabled"] != false || cfg["volume"] != float64(40) {
		t.Fatalf("saved = %v", cfg)
	}
	if resp := e.do(t, "PUT", "/api/settings/notifications", `{"volume":500}`, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("bad volume = %d", resp.StatusCode)
	}
}
