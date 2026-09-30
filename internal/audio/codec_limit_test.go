package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A crafted voice note can make the decoder write far more than the note is worth. The
// output is capped and the process stopped instead of filling memory.
func TestRunStopsAConverterThatWritesTooMuch(t *testing.T) {
	start := time.Now()
	_, err := run(context.Background(), "/bin/sh", nil, "-c", "head -c 100000000 /dev/zero")
	if !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("err = %v, want errOutputTooLarge", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the process was not stopped promptly")
	}
}

func TestRunStillReturnsNormalOutput(t *testing.T) {
	out, err := run(context.Background(), "/bin/sh", nil, "-c", "head -c 3000000 /dev/zero")
	if err != nil || len(out) != 3000000 {
		t.Fatalf("len=%d err=%v", len(out), err)
	}
}
