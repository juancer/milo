package moderation

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampCapsBytesEvenWithoutSpaces(t *testing.T) {
	got := Clamp(strings.Repeat("é", 1<<20), 100)
	if len(got) > MaxReplyBytes || !utf8.ValidString(got) {
		t.Fatalf("len = %d, valid = %v", len(got), utf8.ValidString(got))
	}
	if short := Clamp("Hello there.", 100); short != "Hello there." {
		t.Fatalf("short text changed: %q", short)
	}
}
