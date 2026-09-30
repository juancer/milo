package system

import (
	"log/slog"
	"strings"
	"testing"
)

func TestRingLinesAreBounded(t *testing.T) {
	ring := NewRing(3)
	slog.New(WithRing(slog.NewTextHandler(discard{}, nil), ring)).Info("x", "error", strings.Repeat("é", 1<<20))
	lines := ring.Lines()
	if len(lines) != 1 || len(lines[0]) > 1100 {
		t.Fatalf("ring line of %d bytes", len(lines[0]))
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestMoreSensitiveKeys(t *testing.T) {
	for _, k := range []string{"psk", "Authorization", "Cookie", "bearer", "passwd", "pwd", "wifi_psk", "set-cookie"} {
		if !IsSensitiveKey(k) {
			t.Errorf("%q not treated as sensitive", k)
		}
	}
	for _, k := range []string{"error", "host", "feature"} {
		if IsSensitiveKey(k) {
			t.Errorf("%q wrongly sensitive", k)
		}
	}
}
