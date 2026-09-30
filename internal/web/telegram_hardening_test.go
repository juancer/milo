package web

import (
	"context"
	"testing"
)

func TestUserRoleCannotBecomeOwner(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	uid, _ := e.store.CreateTelegramUser(context.Background(), "Grandma", "FAMILY", 5, "gran")
	body := `{"display_name":"Grandma","role":"OWNER","active":true}`
	if resp := e.do(t, "PUT", "/api/users/"+itoa(uid), body, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("promotion to OWNER = %d, want 400", resp.StatusCode)
	}
	if u, _ := e.store.GetUser(context.Background(), uid); u.Role != "FAMILY" {
		t.Fatalf("role = %s", u.Role)
	}
	// An existing OWNER keeps the role when other fields change.
	oid, _ := e.store.CreateTelegramUser(context.Background(), "Mum", "OWNER", 6, "mum")
	if resp := e.do(t, "PUT", "/api/users/"+itoa(oid), `{"display_name":"Mummy","role":"OWNER","active":true}`, cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("editing an OWNER = %d", resp.StatusCode)
	}
}

func TestChatNamesDifferingOnlyByCaseAreRejected(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	mk := func(name string, id int) string {
		return `{"name":"` + name + `","kind":"group","telegram_chat_id":-` + itoa(int64(id)) + `,"enabled":true}`
	}
	if resp := e.do(t, "POST", "/api/chats", mk("Dad", 1001), cookie, nil); resp.StatusCode != 200 {
		t.Fatalf("first = %d", resp.StatusCode)
	}
	if resp := e.do(t, "POST", "/api/chats", mk("dad", 1002), cookie, nil); resp.StatusCode != 409 {
		t.Fatalf("case-only duplicate = %d, want 409", resp.StatusCode)
	}
}
