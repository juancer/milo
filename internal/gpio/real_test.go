//go:build linux

package gpio

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestRetryBusyWaitsForTheOldLineToClose(t *testing.T) {
	calls := 0
	err := retryBusy(func() error {
		calls++
		if calls < 3 {
			return syscall.EBUSY
		}
		return nil
	}, 5, time.Millisecond)
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want success on the third", err, calls)
	}
	other := errors.New("permission denied")
	calls = 0
	if err := retryBusy(func() error { calls++; return other }, 5, time.Millisecond); err != other || calls != 1 {
		t.Fatalf("other errors must not be retried: %v, %d calls", err, calls)
	}
}
