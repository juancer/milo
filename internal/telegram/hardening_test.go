package telegram_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/telegram"
	"github.com/juancer/milo/internal/telegram/telegramtest"
)

func (h *harness) fireEmergency(id int64) {
	h.tapEmergency(id)
	eventually(h.t, "prompt", func() bool { return h.botSaid(id, "Send emergency alert?") })
	h.fake.Press(id, "emergency:yes")
}

// Only a few messages after an alert are marked urgent, and none once the permission is gone.
func TestUrgentFollowUpsAreCappedAndReauthorized(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(10, "Dad", "PARENT")
	h.fireEmergency(10)
	eventually(t, "alert", func() bool { _, urgent, _ := h.app.Store.PendingCounts(h.ctx); return urgent == 1 })
	for i := 0; i < 6; i++ {
		h.fake.Send(10, "more")
	}
	eventually(t, "all stored", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 7 })
	if _, urgent, _ := h.app.Store.PendingCounts(h.ctx); urgent != 4 {
		t.Fatalf("urgent = %d, want the alert plus 3 follow-ups", urgent)
	}
}

func TestUrgentFollowUpStopsWhenPermissionIsRevoked(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(10, "Dad", "PARENT")
	h.fireEmergency(10)
	eventually(t, "alert", func() bool { _, urgent, _ := h.app.Store.PendingCounts(h.ctx); return urgent == 1 })
	if err := h.app.Perms.Set(h.ctx, dad, "emergency_alert", false); err != nil {
		t.Fatal(err)
	}
	h.fake.Send(10, "still me")
	eventually(t, "stored", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 2 })
	if _, urgent, _ := h.app.Store.PendingCounts(h.ctx); urgent != 1 {
		t.Fatalf("urgent = %d, a revoked user's message was marked critical", urgent)
	}
}

// A user who may not send messages must not make the device download anything.
func TestVoiceFromUserWithoutPermissionIsNotDownloaded(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(20, "Kid", "CHILD")
	h.fake.SendVoice(20, audio.EncodeWAV(audio.Silence(time.Second), 16000), 1)
	eventually(t, "refusal", func() bool { return h.botSaid(20, "not allowed") })
	if n := h.downloads.Load(); n != 0 {
		t.Fatalf("voice downloaded for a user without permission (%d getFile calls)", n)
	}
}

// The bot token is in the URL, so a redirect must not be followed.
func TestDownloadFileDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			w.Write([]byte(`{"ok":true,"result":{"file_path":"voice/a.oga","file_size":3}}`))
			return
		}
		http.Redirect(w, r, other.URL+"/leak", http.StatusFound)
	}))
	defer api.Close()
	if _, err := telegram.NewClient(api.URL, "T").DownloadFile(context.Background(), "x"); err == nil {
		t.Fatal("redirect accepted")
	}
	if hits.Load() != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestDownloadFileRejectsTraversalInFilePath(t *testing.T) {
	var files atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			w.Write([]byte(`{"ok":true,"result":{"file_path":"../../x?y=1","file_size":3}}`))
			return
		}
		files.Add(1)
	}))
	defer api.Close()
	if _, err := telegram.NewClient(api.URL, "T").DownloadFile(context.Background(), "x"); err == nil || files.Load() != 0 {
		t.Fatalf("traversal path was fetched (err=%v)", err)
	}
}

// A person who already has a pending request keeps the invitation.
func TestInvitationIsNotBurnedWhenARequestIsAlreadyPending(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.fake.AddPerson(telegramtest.Person{ID: 30, FirstName: "Cousin", Username: "cousin"})
	first, second := h.invite("FAMILY"), h.invite("FAMILY")
	h.fake.Send(30, "/start "+first)
	eventually(t, "request sent", func() bool { return h.botSaid(30, "sent to the family admin") })
	h.fake.Send(30, "/start "+second)
	eventually(t, "awaiting", func() bool { return h.botSaid(30, "waiting for approval") })
	inv, err := h.app.Store.ConsumeInvitation(h.ctx, telegram.HashToken(second), time.Now())
	if err != nil || inv.ID == 0 {
		t.Fatalf("second invitation was burned: %v", err)
	}
}
