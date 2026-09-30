package telegram_test

import (
	"fmt"
	"testing"

	"github.com/juancer/nexo/internal/telegram"
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
