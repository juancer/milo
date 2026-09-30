package telegram

import (
	"log/slog"
	"strings"
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

// Many different strangers must not each get a warning line: one per gap overall.
func TestStrangerWarningsAreRateLimitedGlobally(t *testing.T) {
	var buf strings.Builder
	now := time.Unix(1000, 0)
	s := New(Deps{Log: slog.New(slog.NewTextHandler(&buf, nil))})
	s.now = func() time.Time { return now }
	for id := int64(1); id <= 50; id++ {
		s.warnStranger(id)
	}
	if n := strings.Count(buf.String(), "unauthorized user"); n != 1 {
		t.Fatalf("%d warnings for one burst, want 1", n)
	}
	now = now.Add(strangerLogGap)
	s.warnStranger(99)
	if n := strings.Count(buf.String(), "unauthorized user"); n != 2 {
		t.Fatalf("%d warnings after the gap, want 2", n)
	}
	if !strings.Contains(buf.String(), "suppressed_since_last=49") {
		t.Fatalf("suppressed count missing: %s", buf.String())
	}
}
