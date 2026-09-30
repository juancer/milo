package audio

import (
	"context"
	"strings"
	"testing"
)

func TestDecodeWAVRejectsOutOfRangeSampleRates(t *testing.T) {
	for rate, ok := range map[int]bool{1: false, 7999: false, 8000: true, 16000: true, 192000: true, 192001: false, 4000000000: false} {
		_, _, err := DecodeWAV(EncodeWAV(make([]byte, 64), rate))
		if (err == nil) != ok {
			t.Errorf("rate %d: err = %v, want ok=%v", rate, err, ok)
		}
	}
}

func TestCappedBufferKeepsOnlyTheFirstBytes(t *testing.T) {
	b := &CappedBuffer{Max: 8}
	for i := 0; i < 3; i++ {
		if n, err := b.Write([]byte("abcdef")); n != 6 || err != nil {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if b.String() != "abcdefab" {
		t.Fatalf("kept %q", b.String())
	}
}

// A converter that floods stderr and then fails must not grow memory without bound,
// and its error text stays short.
func TestRunCapsStderr(t *testing.T) {
	_, err := run(context.Background(), "/bin/sh", nil, "-c", "head -c 5000000 /dev/zero | tr '\\0' e >&2; exit 1")
	if err == nil {
		t.Fatal("expected failure")
	}
	if len(err.Error()) > maxStderr+200 {
		t.Fatalf("error text is %d bytes", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "eeee") {
		t.Fatalf("stderr was lost: %.60q", err.Error())
	}
}
