package system

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRingRedactsInsideGroups(t *testing.T) {
	var out bytes.Buffer
	base, _ := NewLogger(&out, "info", "text")
	ring := NewRing(3)
	slog.New(WithRing(base.Handler(), ring)).Info("x", slog.Group("req", "api_key", "SUPERSECRET", "ok", "visible"))
	got := strings.Join(ring.Lines(), "\n")
	if strings.Contains(got, "SUPERSECRET") || !strings.Contains(got, "req.api_key=[redacted]") || !strings.Contains(got, "req.ok=visible") {
		t.Fatalf("ring = %s", got)
	}
}
