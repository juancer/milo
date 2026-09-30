package web

import "testing"

func TestFailedPasswordChangesDoNotCountAgainstSignIn(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(t)
	for i := 0; i < 6; i++ {
		e.post(t, "/api/account/password", map[string]string{"current_password": "wrong", "new_password": "a-brand-new-password"}, c)
	}
	if code := e.loginFrom("127.0.0.1", "admin", testPassword); code != 200 {
		t.Fatalf("sign-in after failed password changes = %d, want 200", code)
	}
}
