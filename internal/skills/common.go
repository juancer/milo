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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
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
	// Allowed reports whether a feature may be used at a moment (the parents'
	// usage schedules). Nil means always. Actions are checked when called; the
	// skills that speak or play later (timers, reminders, music) ask it again.
	Allowed func(ctx context.Context, feature string, at time.Time) (bool, error)
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

// allowedNow reports whether feature may make sound now. An unreadable
// schedule counts as closed.
func (c Common) allowedNow(ctx context.Context, feature string) bool {
	if c.Allowed == nil {
		return true
	}
	ok, err := c.Allowed(ctx, feature, c.now())
	return err == nil && ok
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
		for runProtected(ctx, loop) && sleepCtx(ctx, restartDelay) {
		}
	}()
}

// restartDelay is the pause before a background loop that panicked is started again.
var restartDelay = 5 * time.Second

// runProtected runs loop and reports whether it panicked. The panic is logged and
// contained, so a bug in one skill's loop cannot take the whole device down.
func runProtected(ctx context.Context, loop func(context.Context)) (panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("skills: background loop panicked; restarting it", "panic", p)
			panicked = true
		}
	}()
	loop(ctx)
	return false
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
// resolve to such addresses are caught when connecting (see httpClient).
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

// refuseLocal is a net.Dialer Control: it refuses to connect to a loopback,
// private (RFC 1918 and IPv6 ULA), link-local, multicast or unspecified address, whether
// the address was typed as an IP or came out of a DNS lookup.
func refuseLocal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("refused: bad address")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("refused: bad address")
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return errors.New("refused: the address points at this device or a private network")
	}
	return nil
}

// httpClient returns the client the skills use for the internet. Unless
// AllowHTTP is set (the simulator talks to local servers) it cannot connect to
// local or private addresses. It ignores proxy settings, which would make the
// checked address the proxy's rather than the destination's.
func (c Common) httpClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !c.AllowHTTP {
		d.Control = refuseLocal
	}
	return &http.Client{Timeout: timeout, CheckRedirect: c.checkRedirect, Transport: &http.Transport{
		DialContext: d.DialContext, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives: true, // a client is built per request, so idle connections would only linger
	}}
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
