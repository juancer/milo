//go:build linux

package gpio

import (
	"context"
	"fmt"
	"time"

	"github.com/warthog618/go-gpiocdev"
)

// Chip is a Controller backed by the Linux GPIO character device
// (/dev/gpiochipN). It needs no CGO and no /dev/mem access.
type Chip struct {
	chip *gpiocdev.Chip
}

// OpenChip opens a GPIO chip such as "gpiochip0".
func OpenChip(name string) (Controller, error) {
	c, err := gpiocdev.NewChip(name)
	if err != nil {
		return nil, fmt.Errorf("gpio: open %s: %w", name, err)
	}
	return &Chip{chip: c}, nil
}

// Watch implements Controller.
func (c *Chip) Watch(ctx context.Context, pin int, pullUp bool, debounce time.Duration, handler func(Event)) error {
	bias := gpiocdev.WithPullDown
	if pullUp {
		bias = gpiocdev.WithPullUp
	}
	line, err := c.chip.RequestLine(pin,
		gpiocdev.AsInput, bias, gpiocdev.WithBothEdges, gpiocdev.WithDebounce(debounce),
		gpiocdev.WithEventHandler(func(e gpiocdev.LineEvent) {
			handler(Event{Pin: pin, High: e.Type == gpiocdev.LineEventRisingEdge, At: time.Now()})
		}))
	if err != nil {
		return fmt.Errorf("gpio: request line %d: %w", pin, err)
	}
	go func() {
		<-ctx.Done()
		line.Close()
	}()
	return nil
}

// Close implements Controller.
func (c *Chip) Close() error { return c.chip.Close() }
