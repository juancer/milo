package buttons

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/gpio"
	"github.com/juancer/nexo/internal/store"
)

type fakeSource struct {
	mu      sync.Mutex
	buttons []store.Button
	err     error
}

func (f *fakeSource) ListButtons(context.Context) ([]store.Button, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]store.Button(nil), f.buttons...), nil
}

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) dispatch(_ context.Context, e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) waitFor(t *testing.T, n int) []Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.events) >= n {
			out := append([]Event(nil), r.events...)
			r.mu.Unlock()
			return out
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("got %d events, want %d: %+v", len(r.events), n, r.events)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

var testOpts = Options{LongPress: 80 * time.Millisecond, DoubleWindow: 120 * time.Millisecond, Debounce: time.Millisecond}

func setup(t *testing.T, buttons ...store.Button) (*Engine, *gpio.Mock, *recorder, *fakeSource) {
	t.Helper()
	m := gpio.NewMock()
	src := &fakeSource{buttons: buttons}
	rec := &recorder{}
	e := New(m, src, rec.dispatch, testOpts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); e.Stop() })
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return e, m, rec, src
}

func btn(id int64, name string, pin int, actions map[string]store.ButtonAction) store.Button {
	return store.Button{ID: id, Name: name, GPIO: pin, ActiveLow: true, Enabled: true, Actions: actions}
}

func TestShortPressMapsToConfiguredAction(t *testing.T) {
	_, m, rec, _ := setup(t,
		btn(1, "A", 17, map[string]store.ButtonAction{ShortPress: {Action: "talk", Parameters: `{"x":1}`}}),
		btn(2, "B", 22, map[string]store.ButtonAction{ShortPress: {Action: "listen"}}),
	)
	m.Tap(22)
	m.Tap(17)
	ev := rec.waitFor(t, 2)
	if ev[0].Action != "listen" || ev[0].ButtonName != "B" || ev[1].Action != "talk" || ev[1].Params["x"] != float64(1) {
		t.Fatalf("events = %+v", ev)
	}
}

func TestSameCodeDifferentInstallations(t *testing.T) {
	// Device C: RED-like button on pin 17 is GPT; nothing else changes.
	_, m, rec, _ := setup(t, btn(1, "Whatever", 17, map[string]store.ButtonAction{ShortPress: {Action: "gpt"}}))
	m.Tap(17)
	if ev := rec.waitFor(t, 1); ev[0].Action != "gpt" {
		t.Fatalf("event = %+v", ev[0])
	}
}

func TestLongPressFiresWhileHeldAndNotShort(t *testing.T) {
	_, m, rec, _ := setup(t, btn(1, "A", 17, map[string]store.ButtonAction{
		ShortPress: {Action: "short"}, LongPress: {Action: "long"},
	}))
	m.Hold(17, 200*time.Millisecond)
	ev := rec.waitFor(t, 1)
	time.Sleep(60 * time.Millisecond)
	if ev[0].Type != LongPress || rec.count() != 1 {
		t.Fatalf("events = %+v", rec.events)
	}
}

func TestShortPressWithLongConfigured(t *testing.T) {
	_, m, rec, _ := setup(t, btn(1, "A", 17, map[string]store.ButtonAction{
		ShortPress: {Action: "short"}, LongPress: {Action: "long"},
	}))
	m.Tap(17)
	if ev := rec.waitFor(t, 1); ev[0].Type != ShortPress {
		t.Fatalf("event = %+v", ev[0])
	}
}

func TestDoublePress(t *testing.T) {
	_, m, rec, _ := setup(t, btn(1, "A", 17, map[string]store.ButtonAction{
		ShortPress: {Action: "short"}, DoublePress: {Action: "double"},
	}))
	m.Tap(17)
	time.Sleep(30 * time.Millisecond)
	m.Tap(17)
	ev := rec.waitFor(t, 1)
	time.Sleep(200 * time.Millisecond)
	if len(ev) != 1 || ev[0].Type != DoublePress || rec.count() != 1 {
		t.Fatalf("events = %+v", rec.events)
	}
}

func TestSingleTapWaitsForDoubleWindow(t *testing.T) {
	_, m, rec, _ := setup(t, btn(1, "A", 17, map[string]store.ButtonAction{
		ShortPress: {Action: "short"}, DoublePress: {Action: "double"},
	}))
	m.Tap(17)
	if rec.count() != 0 {
		t.Fatal("short press fired before the double-press window closed")
	}
	if ev := rec.waitFor(t, 1); ev[0].Type != ShortPress {
		t.Fatalf("event = %+v", ev[0])
	}
}

func TestUnmappedEventsAndDisabledButtonsAreIgnored(t *testing.T) {
	off := btn(2, "Off", 22, map[string]store.ButtonAction{ShortPress: {Action: "x"}})
	off.Enabled = false
	_, m, rec, _ := setup(t,
		btn(1, "A", 17, map[string]store.ButtonAction{LongPress: {Action: "long"}}),
		off,
	)
	m.Tap(17) // only long_press is configured
	m.Tap(22) // disabled
	time.Sleep(150 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatalf("unexpected events: %+v", rec.events)
	}
}

func TestInvalidParametersAreSkipped(t *testing.T) {
	_, m, rec, _ := setup(t, btn(1, "A", 17, map[string]store.ButtonAction{ShortPress: {Action: "x", Parameters: "not json"}}))
	m.Tap(17)
	time.Sleep(100 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatal("event dispatched with invalid parameters")
	}
}

func TestReloadAppliesNewMapping(t *testing.T) {
	e, m, rec, src := setup(t, btn(1, "A", 17, map[string]store.ButtonAction{ShortPress: {Action: "old"}}))
	src.mu.Lock()
	src.buttons = []store.Button{btn(1, "A", 17, map[string]store.ButtonAction{ShortPress: {Action: "new"}})}
	src.mu.Unlock()
	if err := e.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.Tap(17)
	if ev := rec.waitFor(t, 1); ev[0].Action != "new" {
		t.Fatalf("event = %+v", ev[0])
	}
}

func TestFailedReloadKeepsTheRunningButtons(t *testing.T) {
	e, m, rec, src := setup(t, btn(1, "One", 17, map[string]store.ButtonAction{ShortPress: {Action: "a"}}))
	src.mu.Lock()
	src.err = errors.New("store unavailable")
	src.mu.Unlock()
	if err := e.Reload(context.Background()); err == nil {
		t.Fatal("Reload should report the store error")
	}
	m.Tap(17)
	rec.waitFor(t, 1)
}

func TestStaleButtonDoesNotDispatchAfterReload(t *testing.T) {
	e, _, rec, src := setup(t, btn(1, "One", 17, map[string]store.ButtonAction{ShortPress: {Action: "a"}}))
	e.mu.Lock()
	old := e.buttons[1]
	e.mu.Unlock()
	src.mu.Lock()
	src.buttons = nil
	src.mu.Unlock()
	if err := e.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.emit(old, ShortPress)
	if rec.count() != 0 {
		t.Fatal("an emit from a replaced button still dispatched")
	}
}
