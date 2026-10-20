package system

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Ring keeps the most recent log lines in memory for the admin "Logs" view.
// Lines are already redacted, so secrets never reach it.
type Ring struct {
	mu    sync.Mutex
	lines []string
	max   int
	hook  func(line string) // called with every WARN and ERROR line, after redaction (see SetProblemHook)
}

// SetProblemHook makes f receive every warning and error line as it is logged. f must return quickly and
// must not log at WARN or above itself.
func (r *Ring) SetProblemHook(f func(line string)) {
	r.mu.Lock()
	r.hook = f
	r.mu.Unlock()
}

func (r *Ring) problem(line string) {
	r.mu.Lock()
	f := r.hook
	r.mu.Unlock()
	if f != nil {
		f(line)
	}
}

// NewRing keeps at most max lines.
func NewRing(max int) *Ring { return &Ring{max: max} }

// maxLineBytes bounds one ring line, so text a remote party controls (a provider's error message)
// cannot fill the memory of a small device.
const maxLineBytes = 1024

func (r *Ring) add(line string) {
	if len(line) > maxLineBytes {
		cut := maxLineBytes
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + "..."
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = append([]string(nil), r.lines[len(r.lines)-r.max:]...)
	}
}

// Lines returns the buffered lines, oldest first.
func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

type ringHandler struct {
	next  slog.Handler
	ring  *Ring
	attrs []slog.Attr
}

// WithRing wraps a handler so every record is also kept in the ring.
func WithRing(h slog.Handler, r *Ring) slog.Handler { return &ringHandler{next: h, ring: r} }

func (h *ringHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *ringHandler) Handle(ctx context.Context, rec slog.Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-5s %s", rec.Time.Format(time.TimeOnly), rec.Level, rec.Message)
	var write func(prefix string, a slog.Attr)
	write = func(prefix string, a slog.Attr) {
		if v := a.Value.Resolve(); v.Kind() == slog.KindGroup { // redact inside groups too
			if a.Key != "" {
				prefix += a.Key + "."
			}
			for _, g := range v.Group() {
				write(prefix, g)
			}
			return
		}
		v := a.Value.String()
		if IsSensitiveKey(a.Key) {
			v = "[redacted]"
		}
		fmt.Fprintf(&b, " %s%s=%s", prefix, a.Key, v)
	}
	for _, a := range h.attrs {
		write("", a)
	}
	rec.Attrs(func(a slog.Attr) bool { write("", a); return true })
	h.ring.add(b.String())
	if rec.Level >= slog.LevelWarn {
		h.ring.problem(b.String())
	}
	return h.next.Handle(ctx, rec)
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ringHandler{next: h.next.WithAttrs(attrs), ring: h.ring, attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{next: h.next.WithGroup(name), ring: h.ring, attrs: h.attrs}
}
