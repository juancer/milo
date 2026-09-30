package web

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestLocaleEndpoints(t *testing.T) {
	e := newEnv(t, true)

	// Public and English by default: the sign-in page needs it before anyone is authenticated.
	if got := decode[map[string]string](t, e.do(t, "GET", "/api/locale", "", nil, nil)); got["interface_language"] != "en" {
		t.Fatalf("default locale = %v", got)
	}
	if resp := e.do(t, "GET", "/api/locales", "", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("locales without a session = %d, want 401", resp.StatusCode)
	}
	cookie := e.login(t)
	list := decode[[]map[string]string](t, e.do(t, "GET", "/api/locales", "", cookie, nil))
	if len(list) < 2 || list[0]["code"] != "en" || list[0]["name"] != "English" || list[1]["code"] != "es" || list[1]["name"] != "Español" {
		t.Fatalf("locales = %v", list)
	}

	put := func(body string) int { return e.do(t, "PUT", "/api/settings/language", body, cookie, nil).StatusCode }
	if c := put(`{"interface_language":"xx","notification_language":"en"}`); c != 400 {
		t.Fatalf("unsupported language = %d", c)
	}
	if c := put(`{"interface_language":"es"}`); c != 400 {
		t.Fatalf("both languages are required, got %d", c)
	}
	if c := put(`{"interface_language":"es","notification_language":"en"}`); c != 200 {
		t.Fatalf("valid languages = %d", c)
	}
	got := decode[map[string]string](t, e.do(t, "GET", "/api/settings/language", "", cookie, nil))
	if got["interface_language"] != "es" || got["notification_language"] != "en" {
		t.Fatalf("language settings = %v", got)
	}
	if got := decode[map[string]string](t, e.do(t, "GET", "/api/locale", "", nil, nil)); got["interface_language"] != "es" {
		t.Fatalf("public locale after change = %v", got)
	}
}

// API errors follow the interface language and never fail when a translation is missing.
func TestErrorsAreLocalized(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	bad := func() string {
		resp := e.do(t, "POST", "/api/login", `{"username":"admin","password":"wrong-password"}`, nil, nil)
		return decode[map[string]string](t, resp)["error"]
	}
	if got := bad(); got != "incorrect username or password" {
		t.Fatalf("English error = %q", got)
	}
	e.do(t, "PUT", "/api/settings/language", `{"interface_language":"es","notification_language":"es"}`, cookie, nil)
	if got := bad(); got != "usuario o contraseña incorrectos" {
		t.Fatalf("Spanish error = %q", got)
	}
	// Literals the UI relies on are never translated.
	if strings.Contains(bad(), "web.err.") {
		t.Fatal("a raw key leaked to the client")
	}
}

func TestAssistantLanguageSettings(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	get := decode[struct {
		Settings assistantDTO `json:"settings"`
	}](t, e.do(t, "GET", "/api/assistant", "", cookie, nil))
	d := get.Settings
	if d.Language != "en" || d.TTSLanguage != "" || d.TTSSpeed != 1 || d.TTSVolume != 100 {
		t.Fatalf("defaults = %q %q %v %d", d.Language, d.TTSLanguage, d.TTSSpeed, d.TTSVolume)
	}
	put := func(x assistantDTO) int {
		b, _ := json.Marshal(x)
		return e.do(t, "PUT", "/api/assistant", string(b), cookie, nil).StatusCode
	}
	for name, mut := range map[string]func(*assistantDTO){
		"free text":      func(x *assistantDTO) { x.Language = "Spanish" },
		"unknown voice":  func(x *assistantDTO) { x.TTSLanguage = "xx" },
		"speed too high": func(x *assistantDTO) { x.TTSSpeed = 3 },
		"speed too low":  func(x *assistantDTO) { x.TTSSpeed = 0.1 },
		"volume":         func(x *assistantDTO) { x.TTSVolume = 101 },
	} {
		x := d
		mut(&x)
		if c := put(x); c != 400 {
			t.Errorf("%s accepted (%d)", name, c)
		}
	}
	// Interface, assistant and voice languages are independent settings.
	x := d
	x.Language, x.TTSLanguage, x.TTSSpeed, x.TTSVolume = "es", "en", 1.25, 60
	if c := put(x); c != 200 {
		t.Fatalf("valid update = %d", c)
	}
	got := decode[struct {
		Settings assistantDTO `json:"settings"`
	}](t, e.do(t, "GET", "/api/assistant", "", cookie, nil)).Settings
	if got.Language != "es" || got.TTSLanguage != "en" || got.TTSSpeed != 1.25 || got.TTSVolume != 60 {
		t.Fatalf("saved = %+v", got)
	}
	if got := decode[map[string]string](t, e.do(t, "GET", "/api/settings/language", "", cookie, nil)); got["interface_language"] != "en" {
		t.Fatalf("assistant language changed the interface language: %v", got)
	}
	// "auto" follows the child; a body without voice options keeps the stored ones.
	x.Language = "auto"
	b, _ := json.Marshal(x)
	var m map[string]any
	json.Unmarshal(b, &m)
	delete(m, "tts_language")
	delete(m, "tts_speed")
	delete(m, "tts_volume")
	b, _ = json.Marshal(m)
	if resp := e.do(t, "PUT", "/api/assistant", string(b), cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("auto without voice options = %d", resp.StatusCode)
	}
	got = decode[struct {
		Settings assistantDTO `json:"settings"`
	}](t, e.do(t, "GET", "/api/assistant", "", cookie, nil)).Settings
	if got.Language != "auto" || got.TTSLanguage != "en" || got.TTSSpeed != 1.25 || got.TTSVolume != 60 {
		t.Fatalf("after partial update = %+v", got)
	}
}

func TestUserLanguageIsExplicit(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	id, err := e.store.CreateTelegramUser(t.Context(), "Mum", "PARENT", 55, "mum")
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/users/" + strconv.FormatInt(id, 10)
	put := func(lang string) int {
		return e.do(t, "PUT", url, `{"display_name":"Mum","role":"PARENT","active":true,"language":`+lang+`}`, cookie, nil).StatusCode
	}
	if c := put(`"klingon"`); c != 400 {
		t.Fatalf("unsupported user language = %d", c)
	}
	if c := put(`"es"`); c != 200 {
		t.Fatalf("es = %d", c)
	}
	users := decode[[]userDTO](t, e.do(t, "GET", "/api/users", "", cookie, nil))
	if len(users) != 1 || users[0].Language != "es" {
		t.Fatalf("users = %+v", users)
	}
	// A body without a language leaves it alone; an empty one means "follow the default".
	if c := e.do(t, "PUT", url, `{"display_name":"Mum","role":"PARENT","active":true}`, cookie, nil).StatusCode; c != 200 {
		t.Fatalf("update without language = %d", c)
	}
	if u, _ := e.store.GetUser(t.Context(), id); u.Language != "es" {
		t.Fatalf("language lost: %q", u.Language)
	}
	put(`""`)
	if u, _ := e.store.GetUser(t.Context(), id); u.Language != "" {
		t.Fatalf("language not cleared: %q", u.Language)
	}
}
