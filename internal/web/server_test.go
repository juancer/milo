package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/app"
	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/backup"
	"github.com/juancer/nexo/internal/features"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
	"github.com/juancer/nexo/internal/wifi"
)

const testPassword = "a-long-test-password"

type fakeFeature struct{ started bool }

func (f *fakeFeature) Name() string                { return "demo" }
func (f *fakeFeature) Start(context.Context) error { f.started = true; return nil }
func (f *fakeFeature) Stop(context.Context) error  { f.started = false; return nil }

type env struct {
	impl    *server
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
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if _, err := secrets.GenerateKeyFile(keyPath); err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.LoadKey(keyPath)
	vault, err := secrets.New(key, st)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, system.Config{Hardware: "mock", DataDir: t.TempDir(), KeyPath: keyPath}, log, st, vault)
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
		if _, err := st.CreateAdmin(ctx, "admin", hash, true, false); err != nil {
			t.Fatal(err)
		}
	}
	assets := fstest.MapFS{"index.html": {Data: []byte("<h1>Nexo</h1>")}}
	impl := newServer(a, "test")
	srv := httptest.NewServer(impl.handler(assets))
	t.Cleanup(srv.Close)
	return &env{srv: srv, app: a, store: st, feature: ff, impl: impl}
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
	return e.loginAs(t, "admin", testPassword)
}

func (e *env) loginAs(t *testing.T, user, pass string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp := e.do(t, "POST", "/api/login", string(body), nil, nil)
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

func TestSessionInfoReportsWhoIsSignedIn(t *testing.T) {
	e := newEnv(t, true)
	info := decode[map[string]any](t, e.do(t, "GET", "/api/session", "", nil, nil))
	if info["authenticated"] != false || info["username"] != nil {
		t.Fatalf("anonymous session info = %v", info)
	}
	cookie := e.login(t)
	info = decode[map[string]any](t, e.do(t, "GET", "/api/session", "", cookie, nil))
	if info["authenticated"] != true || info["username"] != "admin" || info["is_owner"] != true || info["must_change_password"] != false {
		t.Fatalf("session info = %v", info)
	}
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t, true)
	if resp := e.do(t, "POST", "/api/login", `{"username":"admin","password":"wrong password!"}`, nil, nil); resp.StatusCode != 401 {
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
		e.do(t, "POST", "/api/login", `{"username":"admin","password":"wrong password!"}`, nil, nil)
	}
	// Even the right password is refused while throttled.
	if resp := e.do(t, "POST", "/api/login", `{"username":"admin","password":"`+testPassword+`"}`, nil, nil); resp.StatusCode != 429 {
		t.Fatalf("throttled login = %d, want 429", resp.StatusCode)
	}
}

func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	newHash, _ := auth.HashPassword("another-long-password")
	admin, _ := e.store.GetAdminByName(context.Background(), "admin")
	e.store.SetAdminPassword(context.Background(), admin.ID, newHash, false)
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

func TestSchedulesAPI(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)

	put := func(feature, body string) *http.Response {
		return e.do(t, "PUT", "/api/schedules/"+feature, body, cookie, nil)
	}
	resp := put("telegram", `{"enabled":true,"rules":[
		{"day_of_week":1,"start":"08:00","end":"20:30","enabled":true},
		{"day_of_week":5,"start":"22:00","end":"06:00","enabled":true}]}`)
	got := decode[struct {
		Timezone string `json:"timezone"`
		Features []scheduleDTO
	}](t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("put = %d", resp.StatusCode)
	}
	var tg scheduleDTO
	for _, f := range got.Features {
		if f.Feature == "telegram" {
			tg = f
		}
	}
	if !tg.Enabled || len(tg.Rules) != 2 || tg.Rules[0].Start != "08:00" || tg.Rules[0].End != "20:30" || tg.Rules[1].End != "06:00" {
		t.Fatalf("telegram schedule = %+v", tg)
	}

	// A schedule with the switch on and a window that is not now => closed right now (Monday 08:00 only).
	if put("telegram", `{"enabled":true,"rules":[]}`).StatusCode != 200 {
		t.Fatal("empty window list should be accepted (blocks the feature)")
	}
	allowed, _ := e.app.Schedules.Allowed(context.Background(), "telegram", time.Now())
	if allowed {
		t.Fatal("enabled schedule without windows must block")
	}

	for name, bad := range map[string]string{
		"bad time":        `{"enabled":true,"rules":[{"day_of_week":1,"start":"8am","end":"20:00","enabled":true}]}`,
		"bad day":         `{"enabled":true,"rules":[{"day_of_week":9,"start":"08:00","end":"20:00","enabled":true}]}`,
		"empty window":    `{"enabled":true,"rules":[{"day_of_week":1,"start":"08:00","end":"08:00","enabled":true}]}`,
		"end at midnight": `{"enabled":true,"rules":[{"day_of_week":1,"start":"08:00","end":"00:00","enabled":true}]}`,
		"start at 24":     `{"enabled":true,"rules":[{"day_of_week":1,"start":"24:00","end":"23:00","enabled":true}]}`,
	} {
		if resp := put("telegram", bad); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	if put("ghost", `{"enabled":true,"rules":[]}`).StatusCode != 404 {
		t.Error("unknown feature must be 404")
	}

	// 24:00 is a valid end.
	if put("telegram", `{"enabled":true,"rules":[{"day_of_week":1,"start":"20:00","end":"24:00","enabled":true}]}`).StatusCode != 200 {
		t.Error("window ending at 24:00 rejected")
	}

	if resp := e.do(t, "PUT", "/api/settings/timezone", `{"timezone":"Europe/Madrid"}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("timezone = %d", resp.StatusCode)
	}
	for _, bad := range []string{`{"timezone":"Mars/Olympus"}`, `{"timezone":""}`, `{"timezone":"Local"}`} {
		if resp := e.do(t, "PUT", "/api/settings/timezone", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("timezone %s = %d, want 400", bad, resp.StatusCode)
		}
	}
	entries, _ := e.store.ListAudit(context.Background(), 30)
	audited := false
	for _, en := range entries {
		audited = audited || en.Action == "schedule.updated"
	}
	if !audited {
		t.Fatal("schedule change not audited")
	}
}

func TestSchedulesRequireLogin(t *testing.T) {
	e := newEnv(t, true)
	for _, r := range [][2]string{{"GET", "/api/schedules"}, {"PUT", "/api/schedules/telegram"}, {"PUT", "/api/settings/timezone"}} {
		if resp := e.do(t, r[0], r[1], "{}", nil, nil); resp.StatusCode != 401 {
			t.Errorf("%s %s = %d", r[0], r[1], resp.StatusCode)
		}
	}
}

func TestAssistantSettingsValidationAndKeys(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	get := decode[struct {
		Settings assistantDTO `json:"settings"`
		LLMKey   bool         `json:"llm_key_configured"`
	}](t, e.do(t, "GET", "/api/assistant", "", cookie, nil))
	if get.Settings.LLMModel != "openai/gpt-oss-20b:free" || get.Settings.Age != 7 || get.LLMKey {
		t.Fatalf("defaults = %+v", get)
	}

	d := get.Settings
	d.ChildName, d.Age, d.RetentionDays = "Lia", 9, 7
	body, _ := json.Marshal(d)
	if resp := e.do(t, "PUT", "/api/assistant", string(body), cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("save = %d", resp.StatusCode)
	}
	g, _ := e.store.GetGPTSettings(context.Background())
	if g.ChildName != "Lia" || g.Age != 9 || g.RetentionDays != 7 {
		t.Fatalf("stored = %+v", g)
	}

	bad := map[string]func(*assistantDTO){
		"age":            func(x *assistantDTO) { x.Age = 40 },
		"retention":      func(x *assistantDTO) { x.RetentionDays = 5 },
		"words":          func(x *assistantDTO) { x.MaxResponseWords = 5000 },
		"http url":       func(x *assistantDTO) { x.LLMBaseURL = "ftp://evil.example" },
		"url with creds": func(x *assistantDTO) { x.STTBaseURL = "https://user:pw@evil.example/v1" },
		"model":          func(x *assistantDTO) { x.LLMModel = "x; rm -rf /" },
		"piper path":     func(x *assistantDTO) { x.TTSModelPath = "../../etc/passwd.onnx" },
		"tts engine":     func(x *assistantDTO) { x.TTSEngine = "shell" },
		"empty persona":  func(x *assistantDTO) { x.Personality = " " },
	}
	for name, mutate := range bad {
		x := d
		mutate(&x)
		b, _ := json.Marshal(x)
		if resp := e.do(t, "PUT", "/api/assistant", string(b), cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}

	// Keys are write-only, stored encrypted and never echoed or audited.
	const secret = "sk-or-v1-SUPER-SECRET-VALUE-12345"
	resp := e.do(t, "PUT", "/api/assistant/keys", `{"llm_api_key":"`+secret+`"}`, cookie, nil)
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), `"llm_key_configured":true`) {
		t.Fatalf("set key = %d %s", resp.StatusCode, raw)
	}
	stored, _ := e.store.GetSecret(context.Background(), "assistant.llm_api_key")
	if strings.Contains(string(stored), "SUPER-SECRET") {
		t.Fatal("key stored in plaintext")
	}
	entries, _ := e.store.ListAudit(context.Background(), 50)
	for _, en := range entries {
		if strings.Contains(en.Details+en.Target, "SUPER-SECRET") {
			t.Fatal("key leaked into the audit log")
		}
	}
	for _, badKey := range []string{`{"llm_api_key":"short"}`, `{"llm_api_key":"has space in it here"}`} {
		if resp := e.do(t, "PUT", "/api/assistant/keys", badKey, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("bad key %s = %d", badKey, resp.StatusCode)
		}
	}
	if resp := e.do(t, "DELETE", "/api/assistant/keys/llm", "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("delete key = %d", resp.StatusCode)
	}
	if resp := e.do(t, "DELETE", "/api/assistant/keys/whatever", "", cookie, nil); resp.StatusCode != 404 {
		t.Fatalf("unknown key = %d", resp.StatusCode)
	}
}

func TestModerationRulesAndConversationsAPI(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()

	if resp := e.do(t, "POST", "/api/moderation-rules", `{"pattern":"Voldemort","applies_to":"input"}`, cookie, nil); resp.StatusCode != 201 {
		t.Fatalf("create rule = %d", resp.StatusCode)
	}
	for _, bad := range []string{`{"pattern":"x","applies_to":"both"}`, `{"pattern":"ok phrase","applies_to":"sideways"}`, `{"pattern":"","applies_to":"both"}`} {
		if resp := e.do(t, "POST", "/api/moderation-rules", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d", bad, resp.StatusCode)
		}
	}
	rules := decode[[]map[string]any](t, e.do(t, "GET", "/api/moderation-rules", "", cookie, nil))
	if len(rules) != 1 || rules[0]["pattern"] != "Voldemort" {
		t.Fatalf("rules = %v", rules)
	}
	id := itoa(int64(rules[0]["id"].(float64)))
	if resp := e.do(t, "DELETE", "/api/moderation-rules/"+id, "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("delete rule = %d", resp.StatusCode)
	}

	cid, _ := e.store.CreateConversation(ctx, "button")
	e.store.AddConversationMessage(ctx, cid, "user", "hello there", "")
	e.store.AddConversationMessage(ctx, cid, "assistant", "hi!", "")
	convs := decode[[]map[string]any](t, e.do(t, "GET", "/api/conversations", "", cookie, nil))
	if len(convs) != 1 || len(convs[0]["lines"].([]any)) != 2 {
		t.Fatalf("conversations = %v", convs)
	}
	if resp := e.do(t, "DELETE", "/api/conversations", "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("erase = %d", resp.StatusCode)
	}
	if convs, _ := e.store.ListConversations(ctx, 5); len(convs) != 0 {
		t.Fatal("history not erased")
	}
	for _, r := range [][2]string{{"GET", "/api/assistant"}, {"PUT", "/api/assistant/keys"}, {"GET", "/api/conversations"}, {"GET", "/api/moderation-rules"}} {
		if resp := e.do(t, r[0], r[1], "{}", nil, nil); resp.StatusCode != 401 {
			t.Errorf("%s %s without login = %d", r[0], r[1], resp.StatusCode)
		}
	}
}

func TestWifiNetworksAreEncryptedValidatedAndApplied(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	const pw = "grandma-wifi-pass-123"

	body := `{"ssid":"Grandparents","password":"` + pw + `","priority":5,"enabled":true}`
	resp := e.do(t, "POST", "/api/wifi", body, cookie, nil)
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.Contains(string(raw), pw) {
		t.Fatalf("create = %d %s", resp.StatusCode, raw)
	}
	e.do(t, "POST", "/api/wifi", `{"ssid":"Home","password":"home-wifi-password","priority":9,"enabled":true}`, cookie, nil)
	e.do(t, "POST", "/api/wifi", `{"ssid":"CoffeeShop","open":true,"priority":1,"enabled":true}`, cookie, nil)

	list := decode[struct {
		Status   map[string]any `json:"status"`
		Networks []wifiDTO      `json:"networks"`
	}](t, e.do(t, "GET", "/api/wifi", "", cookie, nil))
	if len(list.Networks) != 3 || list.Networks[0].SSID != "Home" || !list.Networks[2].Open || list.Networks[0].Open {
		t.Fatalf("networks = %+v", list.Networks)
	}
	raw2, _ := json.Marshal(list)
	if strings.Contains(string(raw2), pw) || strings.Contains(string(raw2), "home-wifi-password") {
		t.Fatal("a Wi-Fi password was returned by the API")
	}

	// Stored encrypted, and applied to the operating system (the mock) in priority terms.
	nets, _ := e.store.ListWifiNetworks(ctx)
	for _, n := range nets {
		if n.SecretName == "" {
			continue
		}
		stored, _ := e.store.GetSecret(ctx, n.SecretName)
		if strings.Contains(string(stored), "wifi-pass") || strings.Contains(string(stored), "home-wifi") {
			t.Fatal("Wi-Fi password stored in plaintext")
		}
	}
	applied := e.app.Wifi.(interface{ Applied() []wifi.Network }).Applied()
	if len(applied) != 3 {
		t.Fatalf("applied = %+v", applied)
	}
	byName := map[string]wifi.Network{}
	for _, a := range applied {
		byName[a.SSID] = a
	}
	if byName["Grandparents"].Password != pw || byName["Home"].Priority != 9 || byName["CoffeeShop"].Password != "" {
		t.Fatalf("applied networks = %+v", byName)
	}
	if st := e.app.Wifi.Status(ctx); st.SSID != "Home" {
		t.Fatalf("connected to %q, want the highest priority", st.SSID)
	}

	for name, bad := range map[string]string{
		"empty ssid":     `{"ssid":"","password":"longenoughpw","enabled":true}`,
		"long ssid":      `{"ssid":"` + strings.Repeat("x", 40) + `","password":"longenoughpw","enabled":true}`,
		"short password": `{"ssid":"X","password":"short","enabled":true}`,
		"no password":    `{"ssid":"NoPass","enabled":true}`,
		"priority":       `{"ssid":"Y","password":"longenoughpw","priority":500,"enabled":true}`,
	} {
		if resp := e.do(t, "POST", "/api/wifi", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := e.do(t, "POST", "/api/wifi", `{"ssid":"Home","password":"another-password-1","enabled":true}`, cookie, nil); resp.StatusCode != 409 {
		t.Errorf("duplicate = %d", resp.StatusCode)
	}

	// Keeping the password when editing other fields; removing wipes the secret.
	homeID := itoa(list.Networks[0].ID)
	if resp := e.do(t, "PUT", "/api/wifi/"+homeID, `{"ssid":"Home","priority":2,"enabled":true}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("edit = %d", resp.StatusCode)
	}
	home, _ := e.store.GetWifiNetwork(ctx, list.Networks[0].ID)
	if home.Priority != 2 || home.SecretName == "" {
		t.Fatalf("edit lost the password: %+v", home)
	}
	if resp := e.do(t, "DELETE", "/api/wifi/"+homeID, "", cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if _, err := e.store.GetSecret(ctx, home.SecretName); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("password survived deleting the network")
	}
	if len(e.app.Wifi.(interface{ Applied() []wifi.Network }).Applied()) != 2 {
		t.Fatal("deleted network still applied")
	}
	entries, _ := e.store.ListAudit(ctx, 50)
	for _, en := range entries {
		if strings.Contains(en.Details+en.Target, "wifi-pass") {
			t.Fatal("password in the audit log")
		}
	}
}

func TestWifiApplyFailureIsReportedNotFatal(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	e.app.Wifi.(*wifi.Mock).FailNext(errors.New("nmcli connection failed: no device"))
	resp := e.do(t, "POST", "/api/wifi", `{"ssid":"Home","password":"home-wifi-password","enabled":true}`, cookie, nil)
	out := decode[map[string]any](t, resp)
	if resp.StatusCode != 200 || out["saved"] != true || out["warning"] == nil {
		t.Fatalf("response = %d %v", resp.StatusCode, out)
	}
}

func TestSystemSettingsAndLogs(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	e.app.Logs = system.NewRing(10)
	e.app.Log.Info("hello from the test")

	sys := decode[systemDTO](t, e.do(t, "GET", "/api/system", "", cookie, nil))
	if sys.DeviceName != "Nexo" || sys.RetentionDays != 30 || sys.LongPressMS != 800 || sys.Audio.MixerControl != "Speaker" {
		t.Fatalf("defaults = %+v", sys)
	}
	sys.DeviceName, sys.RetentionDays, sys.AnnounceSender, sys.Audio.MixerControl = "Lia's Nexo", 7, true, "Playback"
	body, _ := json.Marshal(sys)
	saved := decode[systemDTO](t, e.do(t, "PUT", "/api/system", string(body), cookie, nil))
	if saved.DeviceName != "Lia's Nexo" || saved.RetentionDays != 7 || !saved.AnnounceSender || saved.Audio.MixerControl != "Playback" {
		t.Fatalf("saved = %+v", saved)
	}

	for name, mutate := range map[string]func(*systemDTO){
		"empty name":   func(x *systemDTO) { x.DeviceName = "" },
		"retention":    func(x *systemDTO) { x.RetentionDays = 9999 },
		"long press":   func(x *systemDTO) { x.LongPressMS = 10 },
		"shell device": func(x *systemDTO) { x.Audio.PlaybackDevice = "default; rm -rf /" },
		"shell mixer":  func(x *systemDTO) { x.Audio.MixerControl = "Speaker`id`" },
	} {
		x := saved
		mutate(&x)
		b, _ := json.Marshal(x)
		if resp := e.do(t, "PUT", "/api/system", string(b), cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}

	if resp := e.do(t, "POST", "/api/system/restart", "", cookie, nil); resp.StatusCode != 501 {
		t.Errorf("restart without a hook = %d, want 501", resp.StatusCode)
	}
	restarted := make(chan struct{}, 1)
	e.app.RestartFunc = func() { restarted <- struct{}{} }
	if resp := e.do(t, "POST", "/api/system/restart", "", cookie, nil); resp.StatusCode != 202 {
		t.Fatalf("restart = %d", resp.StatusCode)
	}
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("restart hook not called")
	}

	logs := decode[struct{ Lines []string }](t, e.do(t, "GET", "/api/logs", "", cookie, nil))
	_ = logs
	for _, r := range [][2]string{{"GET", "/api/system"}, {"POST", "/api/system/restart"}, {"GET", "/api/logs"}, {"GET", "/api/wifi"}} {
		if resp := e.do(t, r[0], r[1], "{}", nil, nil); resp.StatusCode != 401 {
			t.Errorf("%s %s without login = %d", r[0], r[1], resp.StatusCode)
		}
	}
}

func TestWakeWordSettingsAndSimulatedTrigger(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)

	got := decode[map[string]any](t, e.do(t, "GET", "/api/wakeword", "", cookie, nil))
	settings := got["settings"].(map[string]any)
	if settings["engine"] != "template" || settings["phrase"] != "Hey Nexo" || settings["sensitivity"] != float64(5) || got["simulated"] != true {
		t.Fatalf("defaults = %v", got)
	}
	if resp := e.do(t, "PUT", "/api/wakeword", `{"engine":"template","phrase":"Hola Nexo","sensitivity":7,"external_path":""}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("save = %d", resp.StatusCode)
	}
	for name, bad := range map[string]string{
		"engine":      `{"engine":"cloud","phrase":"x","sensitivity":5}`,
		"phrase":      `{"engine":"template","phrase":"","sensitivity":5}`,
		"sensitivity": `{"engine":"template","phrase":"x","sensitivity":11}`,
		"helper path": `{"engine":"external","phrase":"x","sensitivity":5,"external_path":"/bin/sh; id"}`,
		"missing":     `{"engine":"external","phrase":"x","sensitivity":5,"external_path":"/nonexistent/helper"}`,
	} {
		if resp := e.do(t, "PUT", "/api/wakeword", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	// The simulated microphone cannot record a speaker, so enrolment reports why.
	if resp := e.do(t, "POST", "/api/wakeword/samples", "", cookie, nil); resp.StatusCode != 422 {
		t.Errorf("enrol on mock hardware = %d, want 422", resp.StatusCode)
	}
	if resp := e.do(t, "DELETE", "/api/wakeword/samples/99", "", cookie, nil); resp.StatusCode != 404 {
		t.Errorf("delete missing = %d", resp.StatusCode)
	}
	// The wake word only triggers once its feature is on; then it starts the assistant.
	if resp := e.do(t, "POST", "/api/sim/wake", "", cookie, nil); resp.StatusCode != 409 {
		t.Errorf("wake while feature off = %d, want 409", resp.StatusCode)
	}
	for _, r := range [][2]string{{"GET", "/api/wakeword"}, {"PUT", "/api/wakeword"}, {"POST", "/api/wakeword/samples"}, {"POST", "/api/sim/wake"}} {
		if resp := e.do(t, r[0], r[1], "{}", nil, nil); resp.StatusCode != 401 {
			t.Errorf("%s %s without login = %d", r[0], r[1], resp.StatusCode)
		}
	}
}

func TestSkillsAPI(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()

	// Reminders: created by the admin, validated, blocked words honoured, cancellable.
	future := time.Now().Add(48 * time.Hour).Format("2006-01-02T15:04")
	if resp := e.do(t, "POST", "/api/reminders", `{"text":"Grandma visits","due":"`+future+`"}`, cookie, nil); resp.StatusCode != 201 {
		t.Fatalf("create reminder = %d", resp.StatusCode)
	}
	e.store.CreateModerationRule(ctx, store.ModerationRule{Pattern: "badword", AppliesTo: "both", Enabled: true})
	for name, bad := range map[string]string{
		"past":    `{"text":"x","due":"2001-01-01T10:00"}`,
		"garbage": `{"text":"x","due":"tomorrow"}`,
		"empty":   `{"text":" ","due":"` + future + `"}`,
		"blocked": `{"text":"a badword here","due":"` + future + `"}`,
	} {
		if resp := e.do(t, "POST", "/api/reminders", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	got := decode[struct {
		Reminders []struct {
			ID     int64  `json:"id"`
			Text   string `json:"text"`
			Source string `json:"source"`
		}
		Timers []struct {
			ID int64 `json:"id"`
		}
		CalendarConfigured bool `json:"calendar_configured"`
	}](t, e.do(t, "GET", "/api/skills", "", cookie, nil))
	if len(got.Reminders) != 1 || got.Reminders[0].Source != "admin" || got.CalendarConfigured {
		t.Fatalf("skills = %+v", got)
	}
	if resp := e.do(t, "DELETE", "/api/reminders/"+itoa(got.Reminders[0].ID), "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("cancel reminder = %d", resp.StatusCode)
	}
	if resp := e.do(t, "DELETE", "/api/reminders/999", "", cookie, nil); resp.StatusCode != 404 {
		t.Fatalf("cancel missing = %d", resp.StatusCode)
	}

	// Timers set by the assistant show up and can be cancelled.
	e.app.Features.SetEnabled(ctx, "timer", true)
	e.app.Actions.Execute(ctx, actions.Actor{Kind: actions.ActorAssistant}, "set_timer", map[string]any{"seconds": float64(120), "label": "pasta"})
	got.Timers = nil
	tm := decode[struct{ Timers []struct{ ID int64 } }](t, e.do(t, "GET", "/api/skills", "", cookie, nil))
	if len(tm.Timers) != 1 {
		t.Fatalf("timers = %+v", tm)
	}
	if resp := e.do(t, "DELETE", "/api/timers/"+itoa(tm.Timers[0].ID), "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("cancel timer = %d", resp.StatusCode)
	}

	// Music: folder validation, feature switch required to play.
	dir := t.TempDir()
	for name, bad := range map[string]string{
		"relative": `{"dir":"music"}`, "dotdot": `{"dir":"/tmp/../etc"}`, "missing": `{"dir":"/nonexistent/folder"}`, "shell": `{"dir":"/tmp; rm -rf /"}`,
	} {
		if resp := e.do(t, "PUT", "/api/music", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("music %s = %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := e.do(t, "POST", "/api/music/play", `{"query":"frozen"}`, cookie, nil); resp.StatusCode != 409 {
		t.Errorf("play with the feature off = %d, want 409", resp.StatusCode)
	}
	_ = dir

	// Weather: place lookup goes to the (fake) service; units validated.
	weather := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/search" {
			w.Write([]byte(`{"results":[{"name":"Lisbon","country":"Portugal","latitude":38.72,"longitude":-9.14}]}`))
			return
		}
		w.Write([]byte(`{"current":{"temperature_2m":20,"weather_code":0},"daily":{"weather_code":[0,1],"temperature_2m_max":[24,25],"temperature_2m_min":[15,16],"precipitation_probability_max":[0,0]}}`))
	}))
	defer weather.Close()
	e.store.SetSetting(ctx, "weather.api_base", weather.URL)
	e.store.SetSetting(ctx, "weather.geo_base", weather.URL)
	if resp := e.do(t, "PUT", "/api/weather", `{"place":"Lisbon","units":"celsius"}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("set place = %d", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", "/api/weather", `{"place":"x","units":"kelvin"}`, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("bad units = %d", resp.StatusCode)
	}
	e.app.Features.SetEnabled(ctx, "weather", true)
	res := decode[map[string]any](t, e.do(t, "POST", "/api/weather/test", "", cookie, nil))
	if msg, _ := res["message"].(string); !strings.Contains(msg, "Lisbon, Portugal") || !strings.Contains(msg, "clear") {
		t.Fatalf("weather test = %v", res)
	}

	// Calendar: the secret address is validated, stored encrypted and never returned.
	if resp := e.do(t, "PUT", "/api/calendar", `{"url":"ftp://calendar.example/x.ics"}`, cookie, nil); resp.StatusCode != 400 {
		t.Errorf("ftp calendar = %d", resp.StatusCode)
	}
	secret := "https://calendar.example/private-9f8e7d6c/basic.ics"
	resp := e.do(t, "PUT", "/api/calendar", `{"url":"`+secret+`"}`, cookie, nil)
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.Contains(string(raw), "private-9f8e") || !strings.Contains(string(raw), `"calendar_configured":true`) {
		t.Fatalf("set calendar = %d %s", resp.StatusCode, raw)
	}
	stored, _ := e.store.GetSecret(ctx, "calendar.ics_url")
	if strings.Contains(string(stored), "private-9f8e") {
		t.Fatal("calendar address stored in plaintext")
	}
	entries, _ := e.store.ListAudit(ctx, 60)
	for _, en := range entries {
		if strings.Contains(en.Details+en.Target, "private-9f8e") {
			t.Fatal("calendar address in the audit log")
		}
	}
	if resp := e.do(t, "DELETE", "/api/calendar", "", cookie, nil); resp.StatusCode != 204 {
		t.Fatalf("remove calendar = %d", resp.StatusCode)
	}
	for _, r := range [][2]string{{"GET", "/api/skills"}, {"POST", "/api/reminders"}, {"POST", "/api/music/play"}, {"PUT", "/api/calendar"}, {"POST", "/api/weather/test"}} {
		if resp := e.do(t, r[0], r[1], "{}", nil, nil); resp.StatusCode != 401 {
			t.Errorf("%s %s without login = %d", r[0], r[1], resp.StatusCode)
		}
	}
}

// ---------------------------------------------------------------- multiple administrators

func (e *env) post(t *testing.T, path string, body any, cookie *http.Cookie) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	return e.do(t, "POST", path, string(b), cookie, nil)
}

func TestOwnerAddsAdministratorsWhoMustChooseTheirOwnPassword(t *testing.T) {
	e := newEnv(t, true)
	owner := e.login(t)

	resp := e.post(t, "/api/admins", map[string]string{"username": "mom"}, owner)
	created := decode[map[string]any](t, resp)
	pw, _ := created["generated_password"].(string)
	if resp.StatusCode != 201 || len(pw) != 24 {
		t.Fatalf("create = %d %v", resp.StatusCode, created)
	}
	// Second administrator with a password chosen by the owner: nothing is generated.
	resp = e.post(t, "/api/admins", map[string]string{"username": "dad", "password": "dads-first-password-1"}, owner)
	if c := decode[map[string]any](t, resp); resp.StatusCode != 201 || c["generated_password"] != "" {
		t.Fatalf("create dad = %d %v", resp.StatusCode, c)
	}

	// Mom signs in with the generated password: everything is locked until she changes it.
	mom := e.loginAs(t, "mom", pw)
	info := decode[map[string]any](t, e.do(t, "GET", "/api/session", "", mom, nil))
	if info["must_change_password"] != true || info["is_owner"] != false {
		t.Fatalf("mom's session = %v", info)
	}
	for _, path := range []string{"/api/status", "/api/buttons", "/api/chats", "/api/admins", "/api/system"} {
		r := e.do(t, "GET", path, "", mom, nil)
		body, _ := io.ReadAll(r.Body)
		if r.StatusCode != 403 || !strings.Contains(string(body), "password_change_required") {
			t.Errorf("GET %s before changing the password = %d %s", path, r.StatusCode, body)
		}
	}
	if r := e.post(t, "/api/account/password", map[string]string{"current_password": "wrong", "new_password": "a-brand-new-password"}, mom); r.StatusCode != 401 {
		t.Fatalf("wrong current password = %d", r.StatusCode)
	}
	if r := e.post(t, "/api/account/password", map[string]string{"current_password": pw, "new_password": "short"}, mom); r.StatusCode != 400 {
		t.Fatalf("weak new password = %d", r.StatusCode)
	}
	r := e.post(t, "/api/account/password", map[string]string{"current_password": pw, "new_password": "moms-own-password-42"}, mom)
	var fresh *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == cookieName {
			fresh = c
		}
	}
	if r.StatusCode != 200 || fresh == nil {
		t.Fatalf("change = %d", r.StatusCode)
	}
	// The old session died, the new one works and the lock is gone.
	if r := e.do(t, "GET", "/api/status", "", mom, nil); r.StatusCode != 401 {
		t.Fatalf("old session after password change = %d", r.StatusCode)
	}
	if r := e.do(t, "GET", "/api/status", "", fresh, nil); r.StatusCode != 200 {
		t.Fatalf("new session = %d", r.StatusCode)
	}
	if _, err := e.app.Store.GetAdminByName(context.Background(), "mom"); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "POST", "/api/login", `{"username":"mom","password":"`+pw+`"}`, nil, nil); r.StatusCode != 401 {
		t.Fatalf("the generated password still works: %d", r.StatusCode)
	}
}

func TestOnlyTheOwnerManagesAdministratorsAndOwnerIsProtected(t *testing.T) {
	e := newEnv(t, true)
	owner := e.login(t)
	e.post(t, "/api/admins", map[string]string{"username": "mom", "password": "moms-first-password-1"}, owner)
	// Mom completes her first sign-in so she is not locked.
	mom := e.loginAs(t, "mom", "moms-first-password-1")
	r := e.post(t, "/api/account/password", map[string]string{"current_password": "moms-first-password-1", "new_password": "moms-own-password-42"}, mom)
	for _, c := range r.Cookies() {
		mom = c
	}

	// A regular administrator can see the list and manage everything else, but not the administrators.
	if r := e.do(t, "GET", "/api/admins", "", mom, nil); r.StatusCode != 200 {
		t.Fatalf("mom lists admins = %d", r.StatusCode)
	}
	if r := e.do(t, "GET", "/api/buttons", "", mom, nil); r.StatusCode != 200 {
		t.Fatalf("mom manages buttons = %d", r.StatusCode)
	}
	if r := e.post(t, "/api/admins", map[string]string{"username": "sneaky"}, mom); r.StatusCode != 403 {
		t.Fatalf("mom creates an admin = %d, want 403", r.StatusCode)
	}
	list := decode[[]adminDTO](t, e.do(t, "GET", "/api/admins", "", owner, nil))
	var ownerID, momID int64
	for _, a := range list {
		if a.IsOwner {
			ownerID = a.ID
		} else if a.Username == "mom" {
			momID = a.ID
		}
	}
	if len(list) != 2 || !list[0].IsOwner || !list[0].You {
		t.Fatalf("list = %+v", list)
	}
	if r := e.do(t, "DELETE", "/api/admins/"+itoa(ownerID), "", mom, nil); r.StatusCode != 403 {
		t.Fatalf("mom deletes the owner = %d, want 403", r.StatusCode)
	}
	if r := e.do(t, "DELETE", "/api/admins/"+itoa(ownerID), "", owner, nil); r.StatusCode != 409 {
		t.Fatalf("owner deletes self = %d, want 409", r.StatusCode)
	}
	if r := e.post(t, "/api/admins/"+itoa(momID)+"/reset", nil, mom); r.StatusCode != 403 {
		t.Fatalf("mom resets herself via the owner endpoint = %d", r.StatusCode)
	}

	// Reset signs mom out and demands a new password; deleting removes her at once.
	rr := e.post(t, "/api/admins/"+itoa(momID)+"/reset", nil, owner)
	reset := decode[map[string]any](t, rr)
	if rr.StatusCode != 200 || len(reset["generated_password"].(string)) != 24 {
		t.Fatalf("reset = %d %v", rr.StatusCode, reset)
	}
	if r := e.do(t, "GET", "/api/status", "", mom, nil); r.StatusCode != 401 {
		t.Fatalf("mom's session survived the reset: %d", r.StatusCode)
	}
	mom2 := e.loginAs(t, "mom", reset["generated_password"].(string))
	if r := e.do(t, "DELETE", "/api/admins/"+itoa(momID), "", owner, nil); r.StatusCode != 204 {
		t.Fatalf("delete mom = %d", r.StatusCode)
	}
	if r := e.do(t, "GET", "/api/session", "", mom2, nil); decode[map[string]any](t, r)["authenticated"] != false {
		t.Fatal("a deleted administrator's session still works")
	}
	if r := e.do(t, "DELETE", "/api/admins/"+itoa(momID), "", owner, nil); r.StatusCode != 404 {
		t.Fatalf("delete twice = %d", r.StatusCode)
	}
}

func TestAdminValidationAuditAndSecrecy(t *testing.T) {
	e := newEnv(t, true)
	owner := e.login(t)
	for name, body := range map[string]map[string]string{
		"empty": {"username": ""}, "capital": {"username": "Mom!"}, "space": {"username": "big mom"}, "digit first": {"username": "1mom"},
		"weak password": {"username": "mom", "password": "short"},
	} {
		if r := e.post(t, "/api/admins", body, owner); r.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, r.StatusCode)
		}
	}
	e.post(t, "/api/admins", map[string]string{"username": "mom"}, owner)
	if r := e.post(t, "/api/admins", map[string]string{"username": "mom"}, owner); r.StatusCode != 400 {
		t.Errorf("duplicate = %d", r.StatusCode)
	}
	e.do(t, "POST", "/api/login", `{"username":"mom","password":"wrong-password-here"}`, nil, nil)
	entries, _ := e.store.ListAudit(context.Background(), 50)
	seen := map[string]string{}
	for _, en := range entries {
		seen[en.Action] = en.Actor
		if strings.Contains(en.Details+en.Target, "-") && len(en.Target) > 24 {
			t.Errorf("suspicious audit target %q (looks like a password)", en.Target)
		}
	}
	if seen["admin.created"] != "admin" || seen["admin.login"] != "admin" {
		t.Fatalf("audit actors = %v (entries must name the person who acted)", seen)
	}
}

// loginFrom calls the login handler as a client at addr (the test server only sees one address).
func (e *env) loginFrom(addr, user, pass string) int {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	req.RemoteAddr = addr + ":40000"
	rec := httptest.NewRecorder()
	e.impl.login(rec, req)
	return rec.Code
}

// Failed sign-ins are counted per address. If they were counted per username, anyone on the
// network could lock the owner out by naming "admin".
func TestFailedLoginsFromOneAddressDoNotLockOutTheAccountForOthers(t *testing.T) {
	e := newEnv(t, true)
	for i := 0; i < 5; i++ {
		if code := e.loginFrom("192.0.2.9", "admin", "wrong-password-here"); code != 401 {
			t.Fatalf("attempt %d = %d, want 401", i+1, code)
		}
	}
	if code := e.loginFrom("192.0.2.9", "admin", "wrong-password-here"); code != 429 {
		t.Fatalf("attacker after 5 failures = %d, want 429", code)
	}
	for i := 0; i < 3; i++ { // 8 failures for "admin" in total, from two addresses
		e.loginFrom("192.0.2.10", "admin", "wrong-password-here")
	}
	if code := e.loginFrom("192.0.2.77", "admin", testPassword); code != 200 {
		t.Fatalf("the owner from another address = %d, want 200", code)
	}
}

// A burst of parallel attempts must be counted before the password checks run, not after.
func TestParallelBadLoginsCannotGetPastThePerAddressLimit(t *testing.T) {
	e := newEnv(t, true)
	const burst = 24
	codes := make(chan int, burst)
	for i := 0; i < burst; i++ {
		go func() { codes <- e.loginFrom("192.0.2.9", "nobody", "wrong-password-here") }()
	}
	checked := 0
	for i := 0; i < burst; i++ {
		if <-codes == 401 {
			checked++
		}
	}
	if checked != 5 {
		t.Fatalf("%d passwords were checked, want 5", checked)
	}
}

// ---------------------------------------------------------------- backups

func multipartBody(fields [][2]string, fileName string, file []byte) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		mw.WriteField(f[0], f[1])
	}
	if file != nil {
		part, _ := mw.CreateFormFile("file", fileName)
		part.Write(file)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func (e *env) upload(t *testing.T, path string, body *bytes.Buffer, contentType string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+path, body)
	req.Header.Set(clientHeader, clientHeaderValue)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

const backupPass = "a-good-backup-passphrase"

func downloadBackup(t *testing.T, e *env, cookie *http.Cookie, includeKey bool) []byte {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"passphrase": backupPass, "include_key": includeKey})
	resp := e.do(t, "POST", "/api/backup/download", string(body), cookie, nil)
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "nexo-backup-") {
		t.Fatalf("download = %d %s", resp.StatusCode, data[:min(len(data), 200)])
	}
	return data
}

func TestBackupDownloadIsEncryptedAndComplete(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	e.app.Vault.Put(ctx, "telegram.bot_token", []byte("123456:PLAIN-TEXT-CHECK-abcdefghijklmnop"))
	e.store.InsertMessage(ctx, store.Message{Direction: "in", Kind: "text", Body: "private family message"})

	for _, weak := range []string{"", "short", strings.Repeat("x", 300)} {
		body, _ := json.Marshal(map[string]any{"passphrase": weak})
		if resp := e.do(t, "POST", "/api/backup/download", string(body), cookie, nil); resp.StatusCode != 400 {
			t.Errorf("passphrase %q = %d, want 400", weak, resp.StatusCode)
		}
	}
	withKey := downloadBackup(t, e, cookie, true)
	if bytes.Contains(withKey, []byte("private family message")) || bytes.Contains(withKey, []byte("PLAIN-TEXT-CHECK")) {
		t.Fatal("the downloaded backup is not encrypted")
	}
	c, err := backup.Open(ctx, bytes.NewReader(withKey), backupPass, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Manifest.IncludesKey || len(c.Key) == 0 {
		t.Fatalf("manifest = %+v", c.Manifest)
	}
	if _, err := backup.Open(ctx, bytes.NewReader(withKey), "not the passphrase!!", t.TempDir()); err == nil {
		t.Fatal("opened with the wrong passphrase")
	}
	if c2, err := backup.Open(ctx, bytes.NewReader(downloadBackup(t, e, cookie, false)), backupPass, t.TempDir()); err != nil || c2.Key != nil {
		t.Fatalf("no-key backup: %v", err)
	} else {
		c2.Close()
	}
	entries, _ := e.store.ListAudit(ctx, 40)
	for _, en := range entries {
		if strings.Contains(en.Details+en.Target, backupPass) {
			t.Fatal("the backup passphrase was written to the audit log")
		}
	}
	if resp := e.do(t, "POST", "/api/backup/download", `{"passphrase":"`+backupPass+`"}`, nil, nil); resp.StatusCode != 401 {
		t.Fatalf("download without login = %d", resp.StatusCode)
	}
}

func TestRestoreUploadIsStagedThenAppliedAtRestart(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	e.store.SetSetting(ctx, "family.motto", "before")
	file := downloadBackup(t, e, cookie, true)
	e.store.SetSetting(ctx, "family.motto", "after the backup")

	// Wrong passphrase, wrong order and garbage are refused without touching anything.
	body, ct := multipartBody([][2]string{{"passphrase", "not the right one!!"}}, "b.nexo", file)
	if resp := e.upload(t, "/api/backup/restore", body, ct, cookie); resp.StatusCode != 422 {
		t.Fatalf("wrong passphrase = %d", resp.StatusCode)
	}
	body, ct = multipartBody(nil, "b.nexo", file) // no passphrase at all
	if resp := e.upload(t, "/api/backup/restore", body, ct, cookie); resp.StatusCode != 400 {
		t.Fatalf("missing passphrase = %d", resp.StatusCode)
	}
	body, ct = multipartBody([][2]string{{"passphrase", backupPass}}, "b.nexo", []byte("garbage that is not a backup file at all"))
	if resp := e.upload(t, "/api/backup/restore", body, ct, cookie); resp.StatusCode != 422 {
		t.Fatalf("garbage = %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(e.app.Cfg.DataDir, "restore-pending")); err == nil {
		t.Fatal("something was staged by a refused upload")
	}

	body, ct = multipartBody([][2]string{{"passphrase", backupPass}}, "b.nexo", file)
	resp := e.upload(t, "/api/backup/restore", body, ct, cookie)
	staged := decode[map[string]any](t, resp)
	if resp.StatusCode != 202 || staged["restart_required"] != true {
		t.Fatalf("stage = %d %v", resp.StatusCode, staged)
	}
	if v, _ := e.store.GetSetting(ctx, "family.motto"); v != "after the backup" {
		t.Fatal("the live database changed before the restart")
	}
	// What the next start does:
	applied, prev, err := backup.ApplyPending(e.app.Cfg.DataDir)
	if err != nil || !applied {
		t.Fatalf("apply = %v %q %v", applied, prev, err)
	}
	restored, err := store.Open(ctx, filepath.Join(e.app.Cfg.DataDir, "nexo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if v, _ := restored.GetSetting(ctx, "family.motto"); v != "before" {
		t.Fatalf("restored motto = %q", v)
	}
}

func TestRestoreRefusesABackupFromAnotherDevice(t *testing.T) {
	other := newEnv(t, true) // has its own master key
	otherCookie := other.login(t)
	file := downloadBackup(t, other, otherCookie, true)

	e := newEnv(t, true)
	cookie := e.login(t)
	body, ct := multipartBody([][2]string{{"passphrase", backupPass}}, "b.nexo", file)
	resp := e.upload(t, "/api/backup/restore", body, ct, cookie)
	msg := decode[map[string]string](t, resp)["error"]
	if resp.StatusCode != 422 || !strings.Contains(msg, "different master key") || !strings.Contains(msg, "command line") {
		t.Fatalf("foreign backup = %d %q", resp.StatusCode, msg)
	}
}

func TestOnlyOneRestoreIsCheckedAtATime(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	file := downloadBackup(t, e, cookie, true)
	body, ct := multipartBody([][2]string{{"passphrase", backupPass}}, "b.nexo", file)
	restoreBusy.Lock() // another upload is being checked
	resp := e.upload(t, "/api/backup/restore", body, ct, cookie)
	restoreBusy.Unlock()
	if resp.StatusCode != 409 {
		t.Fatalf("parallel restore = %d, want 409", resp.StatusCode)
	}
	body, ct = multipartBody([][2]string{{"passphrase", backupPass}}, "b.nexo", file)
	if resp := e.upload(t, "/api/backup/restore", body, ct, cookie); resp.StatusCode != 202 {
		t.Fatalf("restore after the other finished = %d", resp.StatusCode)
	}
}

func TestRestorePassphraseGuessingIsThrottled(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	file := downloadBackup(t, e, cookie, false)
	for i := 0; i < 5; i++ {
		body, ct := multipartBody([][2]string{{"passphrase", "wrong passphrase " + fmt.Sprint(i)}}, "b.nexo", file)
		e.upload(t, "/api/backup/restore", body, ct, cookie)
	}
	body, ct := multipartBody([][2]string{{"passphrase", backupPass}}, "b.nexo", file)
	if resp := e.upload(t, "/api/backup/restore", body, ct, cookie); resp.StatusCode != 429 {
		t.Fatalf("after 5 wrong guesses = %d, want 429", resp.StatusCode)
	}
}

func TestLocalBackupSettingsRunAndListing(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	dir := filepath.Join(t.TempDir(), "snapshots")

	got := decode[struct {
		Settings app.BackupSettings `json:"settings"`
		Files    []backup.File      `json:"files"`
	}](t, e.do(t, "GET", "/api/backup", "", cookie, nil))
	// Automatic backups are on by default, and the first one is taken right after the first start.
	if !got.Settings.Auto || got.Settings.Keep != 7 || got.Settings.EveryHours != 24 {
		t.Fatalf("defaults = %+v", got)
	}
	for name, bad := range map[string]string{
		"relative": `{"auto":true,"dir":"backups","keep":3,"every_hours":24}`, "dotdot": `{"auto":true,"dir":"/tmp/../etc","keep":3,"every_hours":24}`,
		"keep 0": `{"auto":true,"dir":"` + dir + `","keep":0,"every_hours":24}`, "every 999": `{"auto":true,"dir":"` + dir + `","keep":3,"every_hours":999}`,
		"unwritable": `{"auto":true,"dir":"/proc/nexo-backups","keep":3,"every_hours":24}`,
	} {
		if resp := e.do(t, "PUT", "/api/backup", bad, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := e.do(t, "PUT", "/api/backup", `{"auto":true,"dir":"`+dir+`","keep":2,"every_hours":12}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("save = %d", resp.StatusCode)
	}
	for i := 0; i < 3; i++ {
		if resp := e.do(t, "POST", "/api/backup/run", "", cookie, nil); resp.StatusCode != 200 {
			t.Fatalf("run = %d", resp.StatusCode)
		}
		time.Sleep(1100 * time.Millisecond) // snapshot names have one-second resolution
	}
	got = decode[struct {
		Settings app.BackupSettings `json:"settings"`
		Files    []backup.File      `json:"files"`
	}](t, e.do(t, "GET", "/api/backup", "", cookie, nil))
	if len(got.Files) != 2 || got.Settings.LastAt.IsZero() || got.Settings.LastError != "" {
		t.Fatalf("after runs = %+v", got)
	}
}

// A backup holds every admin account and, with the key, every secret; a restore replaces the
// account table. A non-owner administrator must not be able to do either.
func TestBackupDownloadAndRestoreAreOwnerOnly(t *testing.T) {
	e := newEnv(t, true)
	owner := e.login(t)
	e.post(t, "/api/admins", map[string]string{"username": "mom", "password": "moms-first-password-1"}, owner)
	mom := e.loginAs(t, "mom", "moms-first-password-1")
	r := e.post(t, "/api/account/password", map[string]string{"current_password": "moms-first-password-1", "new_password": "moms-own-password-42"}, mom)
	for _, c := range r.Cookies() {
		mom = c
	}

	body, _ := json.Marshal(map[string]any{"passphrase": backupPass, "include_key": true})
	if r := e.do(t, "POST", "/api/backup/download", string(body), mom, nil); r.StatusCode != 403 {
		t.Fatalf("download by a non-owner = %d, want 403", r.StatusCode)
	}
	archive := downloadBackup(t, e, owner, false) // the owner still can
	form, ctype := multipartBody([][2]string{{"passphrase", backupPass}}, "b.nexo", archive)
	if r := e.upload(t, "/api/backup/restore", form, ctype, mom); r.StatusCode != 403 {
		t.Fatalf("restore by a non-owner = %d, want 403", r.StatusCode)
	}
}

func TestFeatureToggleIsAuditedPerAccountAndBounded(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	e.do(t, "PUT", "/api/features/demo", `{"enabled":true}`, cookie, nil)
	entries, _ := e.store.ListAudit(context.Background(), 5)
	if entries[0].Action != "feature.enabled" || entries[0].Actor != "admin" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if _, err := e.store.CreateAdmin(context.Background(), "mom", mustHash(t), false, false); err != nil {
		t.Fatal(err)
	}
	mom := e.loginAs(t, "mom", testPassword)
	e.do(t, "PUT", "/api/features/demo", `{"enabled":false}`, mom, nil)
	entries, _ = e.store.ListAudit(context.Background(), 5)
	if entries[0].Actor != "mom" {
		t.Fatalf("actor = %q, want mom", entries[0].Actor)
	}
}

func mustHash(t *testing.T) string {
	t.Helper()
	h, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLoginFailureOnlyNamesExistingAccounts(t *testing.T) {
	e := newEnv(t, true)
	e.do(t, "POST", "/api/login", `{"username":"my secret pass","password":"wrong password!"}`, nil, nil)
	e.do(t, "POST", "/api/login", `{"username":"admin","password":"wrong password!"}`, nil, nil)
	entries, _ := e.store.ListAudit(context.Background(), 5)
	if entries[0].Target != "admin" || entries[1].Target != "" {
		t.Fatalf("targets = %q, %q", entries[0].Target, entries[1].Target)
	}
}

func TestNoDirectoryListing(t *testing.T) {
	e := newEnv(t, false)
	srv := httptest.NewServer(e.impl.handler(fstest.MapFS{"index.html": {Data: []byte("x")}, "_astro/a.js": {Data: []byte("x")}}))
	defer srv.Close()
	for path, want := range map[string]int{"/_astro/": 404, "/_astro/a.js": 200, "/": 200} {
		resp, err := http.Get(srv.URL + path)
		if err != nil || resp.StatusCode != want {
			t.Errorf("%s = %v, %v; want %d", path, resp, err, want)
		}
	}
}

type slowFeature struct{}

func (slowFeature) Name() string                    { return "slow" }
func (slowFeature) Start(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (slowFeature) Stop(context.Context) error      { return nil }

func TestSlowFeatureStartIsBounded(t *testing.T) {
	old := featureTimeout
	featureTimeout = 50 * time.Millisecond
	defer func() { featureTimeout = old }()
	e := newEnv(t, true)
	if err := e.app.Features.Register(context.Background(), slowFeature{}); err != nil {
		t.Fatal(err)
	}
	cookie := e.login(t)
	done := make(chan struct{})
	go func() { e.do(t, "PUT", "/api/features/slow", `{"enabled":true}`, cookie, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("feature switch was not bounded")
	}
}
