package telegram

import (
	"testing"
	"time"
)

// Ids that are all inside the cooldown must not grow the map without bound.
func TestStrangerReplyTableStaysBounded(t *testing.T) {
	s := New(Deps{})
	s.strangerCooldown = time.Hour
	for id := int64(1); id <= 5000; id++ {
		s.strangerCoolingDown(id)
	}
	if n := len(s.replyAt); n > 1001 {
		t.Fatalf("replyAt holds %d entries", n)
	}
}
