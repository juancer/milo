// Package skills holds Nexo's optional abilities: timers, reminders, music,
// weather and calendar. Each skill is a feature (off until a parent turns it
// on) that registers actions; the assistant, buttons and the admin all reach
// them through the Action Engine, so feature switches, schedules and parameter
// validation apply everywhere.
package skills

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/juancer/nexo/internal/store"
)

// Announcer makes the device say (or sound) something at a moment the skill
// chooses, such as "your timer is done". alarm selects the alarm tone over the
// normal message tone.
type Announcer interface {
	Announce(ctx context.Context, text string, alarm bool)
}

// Common holds what every skill needs.
type Common struct {
	Store    *store.Store
	Announce Announcer
	Log      *slog.Logger
	Now      func() time.Time
	// Location returns the device timezone.
	Location func(ctx context.Context) *time.Location
	// AllowHTTP permits plain http:// addresses (development against the simulator).
	AllowHTTP bool
	// Check rejects text a child should not hear (parent-defined blocked phrases).
	Check func(ctx context.Context, text string) error
}

func (c Common) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Common) loc(ctx context.Context) *time.Location {
	if c.Location != nil {
		return c.Location(ctx)
	}
	return time.Local
}

func (c Common) check(ctx context.Context, text string) error {
	if c.Check == nil {
		return nil
	}
	return c.Check(ctx, text)
}

// runner runs a background loop for a skill and stops it cleanly.
type runner struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (r *runner) start(loop func(ctx context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.cancel = cancel
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		loop(ctx)
	}()
}

func (r *runner) stop(ctx context.Context) {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// checkURL accepts https addresses (and http only when AllowHTTP is set).
// CheckURL is checkURL for callers outside the package.
func (c Common) CheckURL(raw string) error { return c.checkURL(raw) }

func (c Common) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("invalid address")
	}
	if u.Scheme == "https" || (u.Scheme == "http" && c.AllowHTTP) {
		return nil
	}
	return fmt.Errorf("the address must start with https://")
}

// checkRedirect is the CheckRedirect of the skills' HTTP clients: a redirect
// must keep to https (see checkURL) and may not point at a loopback, link-local
// or unspecified address given as an IP literal or "localhost". Host names that
// resolve to such addresses are not caught.
func (c Common) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if err := c.checkURL(req.URL.String()); err != nil {
		return err
	}
	h := req.URL.Hostname()
	if ip, err := netip.ParseAddr(h); strings.EqualFold(h, "localhost") ||
		err == nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
		return errors.New("redirect to a local address refused")
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// HumanDuration renders a duration for speech: "5 minutes", "1 hour 5 minutes", "45 seconds".
func HumanDuration(d time.Duration) string {
	secs := int(d.Round(time.Second).Seconds())
	if secs <= 0 {
		return "0 seconds"
	}
	unit := func(n int, name string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s", name)
		}
		return fmt.Sprintf("%d %ss", n, name)
	}
	h, m, s := secs/3600, secs%3600/60, secs%60
	var parts []string
	if h > 0 {
		parts = append(parts, unit(h, "hour"))
	}
	if m > 0 {
		parts = append(parts, unit(m, "minute"))
	}
	if s > 0 && h == 0 {
		parts = append(parts, unit(s, "second"))
	}
	return strings.Join(parts, " ")
}
