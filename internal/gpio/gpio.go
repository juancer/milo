// Package gpio abstracts digital inputs so buttons can be developed and tested
// without a Raspberry Pi.
package gpio

import (
	"context"
	"time"
)

// Event is a level change on a pin.
type Event struct {
	Pin  int
	High bool // level after the change
	At   time.Time
}

// Controller watches input pins.
type Controller interface {
	// Watch delivers debounced level changes for pin to handler until ctx is
	// cancelled. handler must not block. pullUp selects the internal bias
	// (buttons wired to ground use a pull-up).
	Watch(ctx context.Context, pin int, pullUp bool, debounce time.Duration, handler func(Event)) error
	Close() error
}
