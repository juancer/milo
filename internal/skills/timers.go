package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/juancer/milo/internal/actions"
)

const (
	maxTimers        = 10
	missedTimerGrace = 10 * time.Minute
)

// Timers is the "timer" feature: countdowns that survive a reboot.
type Timers struct {
	Common
	Tick time.Duration // how often to look for due timers (default 1s)
	runner
}

// Name implements features.Feature.
func (t *Timers) Name() string { return "timer" }

// Start implements features.Feature.
func (t *Timers) Start(ctx context.Context) error {
	tick := t.Tick
	if tick == 0 {
		tick = time.Second
	}
	t.start(func(ctx context.Context) {
		t.fireDue(ctx, true)
		for sleepCtx(ctx, tick) {
			t.fireDue(ctx, false)
		}
	})
	return nil
}

// Stop implements features.Feature.
func (t *Timers) Stop(ctx context.Context) error { t.stop(ctx); return nil }

// fireDue announces due timers. After a reboot, timers that ran out long ago
// are dropped silently instead of ringing at a random moment. Outside the
// parents' usage window for timers nothing rings: a due timer waits for the
// window to open, and is dropped once it is older than the grace period. A timer
// is marked fired only after it rang, so a stop in between rings it again.
func (t *Timers) fireDue(ctx context.Context, startup bool) {
	due, err := t.Store.DueTimers(ctx, t.now())
	if err != nil {
		t.Log.Warn("timers: cannot read due timers", "error", err)
		return
	}
	if len(due) == 0 {
		return
	}
	open := t.allowedNow(ctx, "timer")
	for _, tm := range due {
		if !open && t.now().Sub(tm.FiresAt) <= missedTimerGrace {
			continue // leave it pending until the window opens
		}
		if !open || startup && t.now().Sub(tm.FiresAt) > missedTimerGrace {
			t.Store.MarkTimerFired(ctx, tm.ID) //nolint:errcheck // dropped without ringing
			continue
		}
		msg := "Your timer is done!"
		if tm.Label != "" {
			msg = fmt.Sprintf("Your %s timer is done!", tm.Label)
		}
		t.Announce.Announce(ctx, msg, true)
		if ctx.Err() != nil {
			return // stopped while ringing: it stays pending and rings again
		}
		t.Store.MarkTimerFired(ctx, tm.ID) //nolint:errcheck // it rings again if this fails
	}
}

// RegisterActions registers the timer actions.
func (t *Timers) RegisterActions(e *actions.Engine) {
	lo, hi := actions.Range(1, 86400)
	both := actions.ExposeDevice | actions.ExposeAssistant
	e.MustRegister(actions.Action{
		Name: "set_timer", Description: "Start a countdown timer that rings when it is done.",
		Feature: "timer", Exposure: both,
		Params: []actions.Param{
			{Name: "seconds", Description: "How long, in seconds (for example 300 for five minutes)", Type: actions.Integer, Required: true, Min: lo, Max: hi},
			{Name: "label", Description: "What the timer is for, for example pasta", Type: actions.String, MaxLen: 40},
		},
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			label := strings.TrimSpace(c.Args.String("label"))
			if err := t.check(ctx, label); err != nil {
				return actions.Result{}, fmt.Errorf("%w: label not allowed", actions.ErrInvalidParams)
			}
			pending, err := t.Store.PendingTimers(ctx)
			if err != nil {
				return actions.Result{}, err
			}
			if len(pending) >= maxTimers {
				return actions.Result{}, fmt.Errorf("%w: there are already %d timers", actions.ErrInvalidParams, maxTimers)
			}
			d := time.Duration(c.Args.Int("seconds")) * time.Second
			if _, err := t.Store.CreateTimer(ctx, label, t.now().Add(d)); err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: "Timer set for " + HumanDuration(d) + "."}, nil
		},
	})
	e.MustRegister(actions.Action{
		Name: "cancel_timer", Description: "Cancel a timer by its label, or every timer if no label is given.",
		Feature: "timer", Exposure: both,
		Params: []actions.Param{{Name: "label", Type: actions.String, MaxLen: 40}},
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			n, err := t.Store.CancelTimers(ctx, strings.TrimSpace(c.Args.String("label")))
			if err != nil {
				return actions.Result{}, err
			}
			if n == 0 {
				return actions.Result{Message: "There was no timer to cancel."}, nil
			}
			return actions.Result{Message: fmt.Sprintf("Cancelled %d timer(s).", n)}, nil
		},
	})
	e.MustRegister(actions.Action{
		Name: "list_timers", Description: "Say which timers are running and how much time is left.",
		Feature: "timer", Exposure: both,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			pending, err := t.Store.PendingTimers(ctx)
			if err != nil {
				return actions.Result{}, err
			}
			if len(pending) == 0 {
				return actions.Result{Message: "No timers are running."}, nil
			}
			var parts []string
			for _, tm := range pending {
				name := "A timer"
				if tm.Label != "" {
					name = "The " + tm.Label + " timer"
				}
				parts = append(parts, fmt.Sprintf("%s has %s left", name, HumanDuration(tm.FiresAt.Sub(t.now()))))
			}
			return actions.Result{Message: strings.Join(parts, ". ") + "."}, nil
		},
	})
}
