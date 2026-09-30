package telegram_test

import (
	"fmt"
	"testing"

	"github.com/juancer/milo/internal/store"
	"github.com/juancer/milo/internal/telegram"
)

// One account must not be able to fill the device with unheard messages.
func TestASenderCannotLeaveUnlimitedPendingMessages(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	cousin := h.addUser(10, "Cousin", "FAMILY")
	dad := h.addUser(11, "Dad", "PARENT")
	const sent = telegram.MaxPendingPerSender + 10
	for i := 0; i < sent; i++ {
		h.fake.Send(10, fmt.Sprintf("hello %d", i))
	}
	// Updates are handled in order, so once Dad's message is stored all of the cousin's were seen.
	h.fake.Send(11, "dinner at 7")
	eventually(t, "everything handled", func() bool {
		n, _, _ := h.app.Store.PendingCounts(h.ctx)
		return n == telegram.MaxPendingPerSender+1
	})
	if n, _ := h.app.Store.PendingFrom(h.ctx, cousin); n != telegram.MaxPendingPerSender {
		t.Fatalf("cousin has %d pending, want %d", n, telegram.MaxPendingPerSender)
	}
	if n, _ := h.app.Store.PendingFrom(h.ctx, dad); n != 1 {
		t.Fatalf("dad has %d pending, want 1", n)
	}
}

// An alert and its urgent follow-ups may exceed the cap only by a small absolute allowance.
func TestEmergencyFollowUpsRespectTheCeiling(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(11, "Dad", "PARENT")
	mum := h.addUser(12, "Mum", "PARENT")
	ceiling := telegram.MaxPendingPerSender + telegram.EmergencyPendingAllowance
	for i := 0; i < ceiling-3; i++ {
		if _, err := h.app.Store.InsertMessage(h.ctx, store.Message{Direction: "in", Kind: "text", Body: "x", UserID: dad}); err != nil {
			t.Fatal(err)
		}
	}
	h.fireEmergency(11)
	eventually(t, "alert", func() bool { _, urgent, _ := h.app.Store.PendingCounts(h.ctx); return urgent == 1 })
	for i := 0; i < 6; i++ {
		h.fake.Send(11, "more")
	}
	h.fake.Send(12, "marker") // handled after Dad's messages
	eventually(t, "marker stored", func() bool { n, _ := h.app.Store.PendingFrom(h.ctx, mum); return n == 1 })
	if n, _ := h.app.Store.PendingFrom(h.ctx, dad); n != ceiling {
		t.Fatalf("dad has %d pending, want exactly the ceiling %d", n, ceiling)
	}
}

// A deactivated sender must not be heard, even for messages queued earlier.
func TestDeactivatedSenderIsNotPlayed(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(11, "Dad", "PARENT")
	if _, err := h.app.Store.InsertMessage(h.ctx, store.Message{Direction: "in", Kind: "text", Body: "hi", UserID: dad}); err != nil {
		t.Fatal(err)
	}
	h.app.Store.UpdateUser(h.ctx, dad, "Dad", "PARENT", false)
	res, err := h.app.Actions.Execute(h.ctx, device(), "listen", nil)
	if err != nil || res.Message != "No messages" {
		t.Fatalf("listen = %q, %v; want No messages", res.Message, err)
	}
	if n, _ := h.app.Store.PendingFrom(h.ctx, dad); n != 0 {
		t.Fatalf("deactivated sender still has %d pending", n)
	}
}

// Losing send_message also silences a sender's queued messages.
func TestSenderWithoutSendMessageIsNotPlayed(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(11, "Dad", "PARENT")
	h.app.Store.InsertMessage(h.ctx, store.Message{Direction: "in", Kind: "text", Body: "hi", UserID: dad})
	h.app.Perms.Set(h.ctx, dad, "send_message", false)
	res, _ := h.app.Actions.Execute(h.ctx, device(), "listen", nil)
	if res.Message != "No messages" {
		t.Fatalf("listen = %q, want No messages", res.Message)
	}
}
