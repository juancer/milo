package web

import (
	"context"
	"testing"
)

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

// Copies of incoming messages are opt-in and only for people who can manage users.
func TestMessageCopiesNeedManageUsersAndDefaultOff(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	uid, _ := e.store.CreateTelegramUser(context.Background(), "Dad", "PARENT", 5, "dad")
	path := "/api/users/" + itoa(uid)
	put := func(body string) (int, userDTO) {
		resp := e.do(t, "PUT", path, body, cookie, nil)
		if resp.StatusCode != 200 {
			return resp.StatusCode, userDTO{}
		}
		return resp.StatusCode, decode[userDTO](t, resp)
	}

	if u := decode[[]userDTO](t, e.do(t, "GET", "/api/users", "", cookie, nil)); u[0].ForwardIncoming || u[0].DeviceAlerts {
		t.Fatalf("new person = %+v, want both switches off", u[0])
	}
	// A PARENT cannot manage users by default, so copies are refused, and nothing is stored.
	if code, _ := put(`{"display_name":"Dad","role":"PARENT","active":true,"forward_incoming":true}`); code != 400 {
		t.Fatalf("copies without manage_users = %d, want 400", code)
	}
	if u, _ := e.store.GetUser(context.Background(), uid); u.ForwardIncoming {
		t.Fatal("copies were stored for someone who cannot manage users")
	}
	// Granting the permission in the same request allows it; leaving the field out keeps it.
	code, u := put(`{"display_name":"Dad","role":"PARENT","active":true,"forward_incoming":true,"permissions":{"manage_users":true}}`)
	if code != 200 || !u.ForwardIncoming {
		t.Fatalf("with manage_users = %d %+v", code, u)
	}
	if code, u = put(`{"display_name":"Dad","role":"PARENT","active":true}`); code != 200 || !u.ForwardIncoming {
		t.Fatalf("an omitted field changed the switch: %d %+v", code, u)
	}
	// Switching off is always allowed.
	if code, u = put(`{"display_name":"Dad","role":"PARENT","active":true,"forward_incoming":false}`); code != 200 || u.ForwardIncoming {
		t.Fatalf("switching off = %d %+v", code, u)
	}
}
