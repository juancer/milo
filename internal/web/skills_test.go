package web

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/juancer/milo/internal/skills"
)

// POST /api/reminders obeys the same feature switch and cap as the create_reminder action.
func TestCreateReminderNeedsTheFeatureAndHonoursTheCap(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	due := time.Now().Add(48 * time.Hour)
	body := `{"text":"again","due":"` + due.Format("2006-01-02T15:04") + `"}`

	if resp := e.do(t, "POST", "/api/reminders", body, cookie, nil); resp.StatusCode != 409 {
		t.Fatalf("with the feature off = %d, want 409", resp.StatusCode)
	}
	if err := e.app.Features.SetEnabled(ctx, "reminders", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < skills.MaxReminders; i++ {
		if _, err := e.store.CreateReminder(ctx, "r"+strconv.Itoa(i), due, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if resp := e.do(t, "POST", "/api/reminders", body, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("over the cap = %d, want 400", resp.StatusCode)
	}
	if p, _ := e.store.PendingReminders(ctx); len(p) != skills.MaxReminders {
		t.Fatalf("%d pending, want %d", len(p), skills.MaxReminders)
	}
}
