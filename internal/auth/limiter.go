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

// maxKeys bounds the fails map so a flood of distinct keys cannot grow it without limit.
const maxKeys = 10000

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
// failure is recorded (Allowed followed by Fail cannot promise that). Call Refund when the
// attempt succeeds.
func (l *Limiter) Try(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	recent := l.recent(key)
	if len(recent) >= l.max || !l.room(key) {
		return false
	}
	l.fails[key] = append(recent, l.now())
	return true
}

// room reports whether key may be stored: it already is, or the map is under maxKeys after
// dropping expired keys. Call it with l.mu held.
func (l *Limiter) room(key string) bool {
	if _, ok := l.fails[key]; ok || len(l.fails) < maxKeys {
		return true
	}
	for k := range l.fails {
		l.recent(k)
	}
	return len(l.fails) < maxKeys
}

// Refund removes the newest attempt recorded for key, undoing the reservation made by Try
// for an attempt that succeeded. Earlier failures stay on record.
func (l *Limiter) Refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.recent(key)
	if len(recent) <= 1 {
		delete(l.fails, key)
		return
	}
	l.fails[key] = recent[:len(recent)-1]
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep()
	recent := l.recent(key)
	if !l.room(key) {
		return
	}
	l.fails[key] = append(recent, l.now())
}

// Reset clears the failures for key after a successful login.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}
