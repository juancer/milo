package auth

import (
	"sync"
	"time"
)

// Limiter throttles repeated failures per key (for example a client IP).
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
	now    func() time.Time
	swept  time.Time
}

// NewLimiter allows max failures per key within window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, fails: map[string][]time.Time{}, now: time.Now}
}

// sweep drops every key whose failures have all expired, at most once per window, so keys
// that are never asked about again do not linger. Call it with l.mu held.
func (l *Limiter) sweep() {
	if l.now().Sub(l.swept) < l.window {
		return
	}
	l.swept = l.now()
	for key := range l.fails {
		l.recent(key)
	}
}

func (l *Limiter) recent(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = kept
	}
	return kept
}

// Allowed reports whether key may attempt again.
func (l *Limiter) Allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) < l.max
}

// Try records an attempt for key and reports whether it may go ahead. The check and the
// count are one step, so a burst of parallel requests cannot all pass before the first
// failure is recorded (Allowed followed by Fail cannot promise that). Call Reset when the
// attempt succeeds.
func (l *Limiter) Try(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	recent := l.recent(key)
	if len(recent) >= l.max {
		return false
	}
	l.fails[key] = append(recent, l.now())
	return true
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	l.fails[key] = append(l.recent(key), l.now())
}

// Reset clears the failures for key after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}
