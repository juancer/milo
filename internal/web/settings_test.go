package web

import "testing"

func TestNotificationSettingsRefuseABodyThatOmitsAField(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	for name, body := range map[string]string{
		"only volume":       `{"volume":40}`,
		"no volume":         `{"new_message_sound_enabled":true,"emergency_sound_enabled":true,"notify_pending_after_schedule":true}`,
		"no emergency flag": `{"new_message_sound_enabled":true,"notify_pending_after_schedule":true,"volume":40}`,
		"empty":             `{}`,
	} {
		if resp := e.do(t, "PUT", "/api/settings/notifications", body, cookie, nil); resp.StatusCode != 400 {
			t.Errorf("%s = %d, want 400", name, resp.StatusCode)
		}
	}
	cfg := decode[map[string]any](t, e.do(t, "GET", "/api/settings/notifications", "", cookie, nil))
	if cfg["emergency_sound_enabled"] != true || cfg["volume"] != float64(100) {
		t.Fatalf("a refused body changed the settings: %v", cfg)
	}
}

func TestWakewordPathIsValidatedEvenForTheTemplateEngine(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	body := `{"engine":"template","phrase":"x","sensitivity":5,"external_path":"/nonexistent/helper"}`
	if resp := e.do(t, "PUT", "/api/wakeword", body, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("invalid path with the template engine = %d, want 400", resp.StatusCode)
	}
}
