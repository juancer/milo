// Package buttons turns raw GPIO level changes into press patterns
// (short, long, double) and resolves them to configured actions.
//
// A button never contains business logic: an event carries the action name and
// parameters read from SQLite, and a Dispatcher hands it to the Action Engine.
package buttons

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/juancer/milo/internal/gpio"
	"github.com/juancer/milo/internal/store"
)

// Event types, matching the values stored in button_actions.event_type.
const (
	ShortPress  = "short_press"
	LongPress   = "long_press"
	DoublePress = "double_press"
)

// Event is a recognised press pattern with its resolved action.
type Event struct {
	ButtonID   int64
	ButtonName string
	Type       string
	Action     string
	Params     map[string]any
}

// Dispatcher receives recognised events. It must not block for long.
type Dispatcher func(ctx context.Context, e Event)

// Source loads button configuration.
type Source interface {
	ListButtons(ctx context.Context) ([]store.Button, error)
}

// Options tune press detection.
type Options struct {
	LongPress    time.Duration // hold time that counts as a long press
	DoubleWindow time.Duration // max gap between two short presses
	Debounce     time.Duration
}

// DefaultOptions are sensible values for tactile buttons.
var DefaultOptions = Options{
	LongPress:    800 * time.Millisecond,
	DoubleWindow: 350 * time.Millisecond,
	Debounce:     10 * time.Millisecond,
}

// Engine watches the configured pins.
type Engine struct {
	gpio     gpio.Controller
	src      Source
	dispatch Dispatcher
	opts     Options
	log      *slog.Logger

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	buttons map[int64]*buttonState
}

type buttonState struct {
	cfg       store.Button
	mu        sync.Mutex
	down      bool
	longFired bool
	longTimer *time.Timer
	shortWait *time.Timer // pending short press awaiting a possible second tap
}

// New creates an Engine. Call Start to begin watching.
func New(g gpio.Controller, src Source, d Dispatcher, opts Options, log *slog.Logger) *Engine {
	return &Engine{gpio: g, src: src, dispatch: d, opts: opts, log: log, buttons: map[int64]*buttonState{}}
}

// Start loads the configuration and starts watching enabled buttons.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ctx = ctx
	return e.reloadLocked()
}

// Reload re-reads the configuration; call it after buttons change.
func (e *Engine) Reload(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx == nil {
		return fmt.Errorf("buttons: engine not started")
	}
	return e.reloadLocked()
}

// SetDispatcher replaces the dispatcher. Used by `milo test-buttons` to print
// events instead of running their actions.
func (e *Engine) SetDispatcher(d Dispatcher) {
	e.mu.Lock()
	e.dispatch = d
	e.mu.Unlock()
}

// Stop stops watching all pins.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLocked()
}

func (e *Engine) stopLocked() {
	if e.cancel != nil {
		e.cancel()
	}
	for _, b := range e.buttons {
		b.stop()
	}
	e.buttons = map[int64]*buttonState{}
}

func (e *Engine) reloadLocked() error {
	// Read the configuration first: a store error must leave the running
	// buttons watched rather than stop them all.
	configs, err := e.src.ListButtons(e.ctx)
	if err != nil {
		return err
	}
	e.stopLocked()
	watchCtx, cancel := context.WithCancel(e.ctx)
	e.cancel = cancel
	var firstErr error
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		st := &buttonState{cfg: cfg}
		e.buttons[cfg.ID] = st
		handler := func(ev gpio.Event) { e.onGPIO(st, ev) }
		if err := e.gpio.Watch(watchCtx, cfg.GPIO, cfg.ActiveLow, e.opts.Debounce, handler); err != nil {
			e.log.Error("cannot watch button", "button", cfg.Name, "gpio", cfg.GPIO, "error", err)
			delete(e.buttons, cfg.ID)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	e.log.Info("buttons ready", "watching", len(e.buttons))
	return firstErr
}

func (b *buttonState) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.longTimer != nil {
		b.longTimer.Stop()
	}
	if b.shortWait != nil {
		b.shortWait.Stop()
	}
}

func (e *Engine) onGPIO(b *buttonState, ev gpio.Event) {
	pressed := ev.High != b.cfg.ActiveLow
	b.mu.Lock()
	defer b.mu.Unlock()
	if pressed {
		if b.down {
			return
		}
		b.down, b.longFired = true, false
		if _, ok := b.cfg.Actions[LongPress]; ok {
			b.longTimer = time.AfterFunc(e.opts.LongPress, func() {
				b.mu.Lock()
				if !b.down {
					b.mu.Unlock()
					return
				}
				b.longFired = true
				b.mu.Unlock()
				e.emit(b, LongPress)
			})
		}
		return
	}
	if !b.down {
		return
	}
	b.down = false
	if b.longTimer != nil {
		b.longTimer.Stop()
	}
	if b.longFired {
		return
	}
	// A short press. Only wait for a second tap if a double press is configured,
	// so single-action buttons stay instant.
	if _, hasDouble := b.cfg.Actions[DoublePress]; !hasDouble {
		go e.emit(b, ShortPress)
		return
	}
	if b.shortWait != nil && b.shortWait.Stop() {
		b.shortWait = nil
		go e.emit(b, DoublePress)
		return
	}
	b.shortWait = time.AfterFunc(e.opts.DoubleWindow, func() {
		b.mu.Lock()
		b.shortWait = nil
		b.mu.Unlock()
		e.emit(b, ShortPress)
	})
}

// emit resolves the configured action and dispatches it.
func (e *Engine) emit(b *buttonState, eventType string) {
	// emit runs on timer and event goroutines: a panic in a dispatcher must not
	// take the process down.
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("panic contained", "where", "button "+b.cfg.Name, "event", eventType, "panic", fmt.Sprint(r))
		}
	}()
	a, ok := b.cfg.Actions[eventType]
	if !ok {
		return
	}
	params := map[string]any{}
	if a.Parameters != "" {
		if err := json.Unmarshal([]byte(a.Parameters), &params); err != nil {
			e.log.Warn("button has invalid parameters", "button", b.cfg.Name, "event", eventType, "error", err)
			return
		}
	}
	e.mu.Lock()
	ctx, dispatch := e.ctx, e.dispatch
	current := e.buttons[b.cfg.ID] == b // false once a Reload or Stop replaced it
	e.mu.Unlock()
	if !current || ctx == nil || ctx.Err() != nil {
		return
	}
	dispatch(ctx, Event{ButtonID: b.cfg.ID, ButtonName: b.cfg.Name, Type: eventType, Action: a.Action, Params: params})
}
