package gpio

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Mock is an in-memory Controller. Tests and the development simulator drive it
// with Press/Release.
type Mock struct {
	mu       sync.Mutex
	watchers map[int][]*mockWatch
	levels   map[int]bool // true = high
}

type mockWatch struct {
	handler func(Event)
	ctx     context.Context
}

// NewMock returns a Mock with all pins high (released for active-low buttons).
func NewMock() *Mock {
	return &Mock{watchers: map[int][]*mockWatch{}, levels: map[int]bool{}}
}

// Watch implements Controller.
func (m *Mock) Watch(ctx context.Context, pin int, pullUp bool, _ time.Duration, handler func(Event)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.levels[pin]; !ok {
		m.levels[pin] = pullUp
	}
	m.watchers[pin] = append(m.watchers[pin], &mockWatch{handler: handler, ctx: ctx})
	return nil
}

// Close implements Controller.
func (m *Mock) Close() error { return nil }

// Set drives a pin to a level and notifies active watchers.
func (m *Mock) Set(pin int, high bool) {
	m.mu.Lock()
	if m.levels[pin] == high {
		m.mu.Unlock()
		return
	}
	m.levels[pin] = high
	var active []*mockWatch
	kept := m.watchers[pin][:0]
	for _, w := range m.watchers[pin] {
		if w.ctx.Err() == nil {
			active = append(active, w)
			kept = append(kept, w)
		}
	}
	m.watchers[pin] = kept
	m.mu.Unlock()
	for _, w := range active {
		w.handler(Event{Pin: pin, High: high, At: time.Now()})
	}
}

// Press drives an active-low button: the line goes low.
func (m *Mock) Press(pin int) { m.Set(pin, false) }

// Release returns an active-low button line to high.
func (m *Mock) Release(pin int) { m.Set(pin, true) }

// Hold presses, waits d, then releases. It blocks for d.
func (m *Mock) Hold(pin int, d time.Duration) {
	m.Press(pin)
	time.Sleep(d)
	m.Release(pin)
}

// Tap is a short press.
func (m *Mock) Tap(pin int) { m.Hold(pin, 20*time.Millisecond) }

func (m *Mock) String() string { return fmt.Sprintf("gpio.Mock(%d pins)", len(m.levels)) }
