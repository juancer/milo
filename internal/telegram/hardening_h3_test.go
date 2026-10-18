package telegram_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juancer/milo/internal/store"
	"github.com/juancer/milo/internal/telegram"
)

// Many approved senders together must not exceed the global pending ceiling.
func TestPendingMessagesHaveAGlobalCeiling(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(11, "Dad", "PARENT")
	filler := h.addUser(12, "Filler", "FAMILY")
	for i := 0; i < telegram.MaxPendingTotal; i++ {
		if _, err := h.app.Store.InsertMessage(h.ctx, store.Message{Direction: "in", Kind: "text", Body: "x", UserID: filler}); err != nil {
			t.Fatal(err)
		}
	}
	h.fake.Send(11, "one more")
	eventually(t, "refusal", func() bool { return h.botSaid(11, "too many") })
	if n, _ := h.app.Store.PendingFrom(h.ctx, dad); n != 0 {
		t.Fatalf("dad has %d pending despite the global ceiling", n)
	}
	// An emergency alert still gets through.
	h.fireEmergency(11)
	eventually(t, "alert", func() bool { _, urgent, _ := h.app.Store.PendingCounts(h.ctx); return urgent == 1 })
}

// An outage must not let the outbound queue grow without bound.
func TestOutboundQueueIsCapped(t *testing.T) {
	h := newHarness(t, true)

	mk := func(name string, tg int64) int64 {
		id, err := h.app.Store.CreateChat(h.ctx, store.Chat{Name: name, Kind: "group", TelegramChatID: tg, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	dad, mum := mk("Dad", -1), mk("Mum", -2)
	later := time.Now().Add(time.Hour) // keeps the rows queued: the sender never picks them up
	fill := func(chat int64, n int) {
		for i := 0; i < n; i++ {
			id, err := h.app.Store.InsertMessage(h.ctx, store.Message{Direction: "out", Kind: "text", ChatID: chat, Body: "x"})
			if err != nil {
				t.Fatal(err)
			}
			h.app.Store.MarkAttemptFailed(h.ctx, id, "offline", &later) //nolint:errcheck
		}
	}
	fill(dad, telegram.MaxQueuedPerChat)
	h.enable() // only now: while the feature runs, the sender could pick a row up before it is deferred
	send := func(chat string) string {
		res, err := h.app.Actions.Execute(h.ctx, device(), "send_message", map[string]any{"chat": chat, "text": "hi"})
		if err != nil {
			t.Fatal(err)
		}
		return res.Message
	}
	if got := send("Dad"); !strings.Contains(got, "Not queued") {
		t.Fatalf("full chat: %q, want a refusal", got)
	}
	if got := send("Mum"); !strings.Contains(got, "queued for Mum") {
		t.Fatalf("other chat: %q, want it queued", got)
	}
	fill(mum, telegram.MaxQueuedTotal) // pushes the total over the global cap
	if got := send("Mum"); !strings.Contains(got, "Not queued") {
		t.Fatalf("global cap: %q, want a refusal", got)
	}
}
