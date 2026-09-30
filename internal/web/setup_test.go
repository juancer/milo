package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestFirstRunSetupFlow(t *testing.T) {
	e := newEnv(t, false)

	info := decode[map[string]any](t, e.do(t, "GET", "/api/session", "", nil, nil))
	if info["authenticated"] != false || info["setup_required"] != true {
		t.Fatalf("fresh session info = %v", info)
	}
	// Nothing else is reachable before setup.
	if r := e.do(t, "GET", "/api/status", "", nil, nil); r.StatusCode != 401 {
		t.Fatalf("status before setup = %d", r.StatusCode)
	}
	// Bad input is refused, in the interface language, and creates nothing.
	if r := e.do(t, "POST", "/api/setup", `{"username":"admin","password":"short"}`, nil, nil); r.StatusCode != 400 {
		t.Fatalf("weak password = %d", r.StatusCode)
	}
	if r := e.do(t, "POST", "/api/setup", `{"username":"A B","password":"`+testPassword+`"}`, nil, nil); r.StatusCode != 400 {
		t.Fatalf("bad username = %d", r.StatusCode)
	}
	// The CSRF header is still required.
	if r := e.do(t, "POST", "/api/setup", `{"username":"admin","password":"`+testPassword+`"}`, nil, map[string]string{clientHeader: ""}); r.StatusCode != 403 {
		t.Fatalf("setup without the client header = %d", r.StatusCode)
	}

	resp := e.do(t, "POST", "/api/setup", `{"username":"Owner","password":"`+testPassword+`"}`, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("setup = %d", resp.StatusCode)
	}
	got := decode[map[string]any](t, resp)
	if got["username"] != "owner" || got["is_owner"] != true || got["must_change_password"] != false {
		t.Fatalf("setup answer = %v", got)
	}
	cookie := sessionCookie(t, resp)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie not hardened: %+v", cookie)
	}
	if r := e.do(t, "GET", "/api/status", "", cookie, nil); r.StatusCode != 200 {
		t.Fatalf("status with the setup session = %d", r.StatusCode)
	}
	info = decode[map[string]any](t, e.do(t, "GET", "/api/session", "", nil, nil))
	if _, has := info["setup_required"]; has {
		t.Fatalf("setup_required still offered: %v", info)
	}
	// It never works twice, and the chosen password logs in.
	if r := e.do(t, "POST", "/api/setup", `{"username":"intruder","password":"`+testPassword+`"}`, nil, nil); r.StatusCode != 409 {
		t.Fatalf("second setup = %d, want 409", r.StatusCode)
	}
	e.loginAs(t, "owner", testPassword)
	entries, _ := e.store.ListAudit(t.Context(), 10)
	found := false
	for _, a := range entries {
		found = found || a.Action == "admin.setup"
	}
	if !found {
		t.Fatal("setup was not audited")
	}
}

func TestSetupRaceCreatesOneOwner(t *testing.T) {
	e := newEnv(t, false)
	codes := make(chan int, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- e.do(t, "POST", "/api/setup", `{"username":"admin","password":"`+testPassword+`"}`, nil, nil).StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == 200 {
			ok++
		} else if c != 409 && c != 503 && c != 429 {
			t.Errorf("unexpected status %d", c)
		}
	}
	if n, _ := e.store.AdminCount(t.Context()); n != 1 || ok != 1 {
		t.Fatalf("admins = %d, 200s = %d; want 1 and 1", n, ok)
	}
}

// Setup makes the caller the owner, so it is limited to the home network, judged by the TCP peer only.
func TestSetupOnlyFromTheHomeNetwork(t *testing.T) {
	e := newEnv(t, false)
	h := e.impl.handler(fstest.MapFS{"index.html": {Data: []byte("x")}})
	body := `{"username":"owner","password":"` + testPassword + `"}`
	try := func(remote string, headers map[string]string) int {
		req := httptest.NewRequest("POST", "/api/setup", strings.NewReader(body))
		req.RemoteAddr = remote
		req.Header.Set(clientHeader, clientHeaderValue)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, remote := range []string{"8.8.8.8:5000", "[2001:4860:4860::8888]:5000", "100.64.0.1:5000"} {
		if code := try(remote, nil); code != http.StatusForbidden {
			t.Errorf("setup from %s = %d, want 403", remote, code)
		}
	}
	// A forwarding header cannot make a public peer look local.
	if code := try("8.8.8.8:5000", map[string]string{"X-Forwarded-For": "192.168.1.5", "X-Real-IP": "10.0.0.2"}); code != http.StatusForbidden {
		t.Errorf("spoofed X-Forwarded-For = %d, want 403", code)
	}
	if info := decode[map[string]any](t, e.do(t, "GET", "/api/session", "", nil, nil)); info["setup_required"] != true {
		t.Fatalf("a refused setup changed the state: %v", info)
	}
	// Private, loopback, link-local and ULA peers are accepted (only the first one can create the owner).
	for _, remote := range []string{"127.0.0.1:5000", "192.168.1.20:5000", "10.1.2.3:5000", "172.20.0.4:5000", "169.254.1.1:5000", "[fd12:3456::1]:5000", "[fe80::1]:5000"} {
		if !fromHomeNetwork(&http.Request{RemoteAddr: remote}) {
			t.Errorf("%s should count as the home network", remote)
		}
	}
	if code := try("192.168.1.20:5000", nil); code != http.StatusOK {
		t.Fatalf("setup from a private peer = %d", code)
	}
}
