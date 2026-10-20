package system

import (
	"bytes"
	"io"
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

func TestProblemHookGetsWarningsAndErrorsOnly(t *testing.T) {
	ring := NewRing(10)
	var got []string
	ring.SetProblemHook(func(line string) { got = append(got, line) })
	log := slog.New(WithRing(slog.NewTextHandler(io.Discard, nil), ring))
	log.Info("fine")
	log.Warn("careful", "token", "abc123", "reason", "slow")
	log.Error("broken")
	if len(got) != 2 || !strings.Contains(got[0], "WARN") || !strings.Contains(got[1], "ERROR broken") {
		t.Fatalf("hook lines = %q", got)
	}
	if strings.Contains(got[0], "abc123") || !strings.Contains(got[0], "reason=slow") {
		t.Fatalf("the line was not redacted by key: %q", got[0])
	}
}
