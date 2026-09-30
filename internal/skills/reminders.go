package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/actions"
)

const maxReminders = 50 // pending reminders

// MaxReminders is maxReminders for callers outside the package.
const MaxReminders = maxReminders

// Reminders is the "reminders" feature: things to say at a time, kept across reboots.
type Reminders struct {
	Common
	Tick time.Duration
	runner
}

// Name implements features.Feature.
func (r *Reminders) Name() string { return "reminders" }

// Start implements features.Feature.
func (r *Reminders) Start(ctx context.Context) error {
	tick := r.Tick
	if tick == 0 {
		tick = 5 * time.Second
	}
	r.start(func(ctx context.Context) {
		r.fireDue(ctx)
		for sleepCtx(ctx, tick) {
			r.fireDue(ctx)
		}
	})
	return nil
}

// Stop implements features.Feature.
func (r *Reminders) Stop(ctx context.Context) error { r.stop(ctx); return nil }

// fireDue announces due reminders, marking each done only after it was said, so
// a stop in between says it again on the next start instead of losing it. Outside the parents' usage window for
// reminders they stay pending and are said once the window opens.
func (r *Reminders) fireDue(ctx context.Context) {
	due, err := r.Store.DueReminders(ctx, r.now())
	if err != nil {
		r.Log.Warn("reminders: cannot read due reminders", "error", err)
		return
	}
	if len(due) == 0 || !r.allowedNow(ctx, "reminders") {
		return
	}
	for _, rem := range due {
		r.Announce.Announce(ctx, "Reminder: "+rem.Text, false)
		if ctx.Err() != nil {
			return // stopped while speaking: it stays pending
		}
		r.Store.MarkReminderDone(ctx, rem.ID) //nolint:errcheck // it is announced again if this fails
	}
}

// NextClock returns the next moment the local clock reads hh:mm, today if that
// is still ahead and tomorrow otherwise.
func NextClock(now time.Time, hh, mm int, loc *time.Location) time.Time {
	local := now.In(loc)
	t := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, loc)
	if !t.After(local) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// RegisterActions registers the reminder actions.
func (r *Reminders) RegisterActions(e *actions.Engine) {
	lo, hi := actions.Range(10, 60*86400)
	both := actions.ExposeDevice | actions.ExposeAssistant
	e.MustRegister(actions.Action{
		Name: "create_reminder", Description: "Remind the child of something later. Give either in_seconds (how long from now) or at_time (a clock time such as 17:30).",
		Feature: "reminders", Exposure: both,
		Params: []actions.Param{
			{Name: "text", Description: "What to remind, for example brush your teeth", Type: actions.String, Required: true, MaxLen: 200},
			{Name: "in_seconds", Description: "How many seconds from now", Type: actions.Integer, Min: lo, Max: hi},
			{Name: "at_time", Description: "Clock time today or tomorrow, 24-hour HH:MM", Type: actions.String, MaxLen: 5},
		},
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			text := strings.TrimSpace(c.Args.String("text"))
			if text == "" {
				return actions.Result{}, fmt.Errorf("%w: text is empty", actions.ErrInvalidParams)
			}
			if err := r.check(ctx, text); err != nil {
				return actions.Result{}, fmt.Errorf("%w: that text is not allowed", actions.ErrInvalidParams)
			}
			if c.Args.Has("in_seconds") == c.Args.Has("at_time") {
				return actions.Result{}, fmt.Errorf("%w: give exactly one of in_seconds or at_time", actions.ErrInvalidParams)
			}
			pending, err := r.Store.PendingReminders(ctx)
			if err != nil {
				return actions.Result{}, err
			}
			if len(pending) >= maxReminders {
				return actions.Result{}, fmt.Errorf("%w: there are already %d reminders", actions.ErrInvalidParams, maxReminders)
			}
			var due time.Time
			var when string
			if c.Args.Has("in_seconds") {
				d := time.Duration(c.Args.Int("in_seconds")) * time.Second
				due, when = r.now().Add(d), "in "+HumanDuration(d)
			} else {
				var hh, mm int
				if _, err := fmt.Sscanf(c.Args.String("at_time"), "%d:%d", &hh, &mm); err != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
					return actions.Result{}, fmt.Errorf("%w: at_time must be HH:MM", actions.ErrInvalidParams)
				}
				due = NextClock(r.now(), hh, mm, r.loc(ctx))
				when = "at " + due.In(r.loc(ctx)).Format("15:04")
			}
			source := "assistant"
			if c.Actor.Kind == actions.ActorDevice {
				source = "button"
			}
			if _, err := r.Store.CreateReminder(ctx, text, due, source); err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: "I will remind you " + when + "."}, nil
		},
	})
	e.MustRegister(actions.Action{
		Name: "list_reminders", Description: "Say what reminders are waiting.",
		Feature: "reminders", Exposure: both,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			pending, err := r.Store.PendingReminders(ctx)
			if err != nil {
				return actions.Result{}, err
			}
			if len(pending) == 0 {
				return actions.Result{Message: "You have no reminders."}, nil
			}
			var parts []string
			for _, rem := range pending {
				parts = append(parts, fmt.Sprintf("%s at %s", rem.Text, rem.DueAt.In(r.loc(ctx)).Format("Mon 15:04")))
			}
			return actions.Result{Message: "You have: " + strings.Join(parts, "; ") + "."}, nil
		},
	})
	e.MustRegister(actions.Action{
		Name: "cancel_reminder", Description: "Cancel reminders whose text contains the given words.",
		Feature: "reminders", Exposure: both,
		Params: []actions.Param{{Name: "text", Type: actions.String, Required: true, MaxLen: 200}},
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			match := strings.TrimSpace(c.Args.String("text"))
			if match == "" { // an empty match would cancel every reminder
				return actions.Result{}, fmt.Errorf("%w: text is empty", actions.ErrInvalidParams)
			}
			n, err := r.Store.CancelReminders(ctx, match)
			if err != nil {
				return actions.Result{}, err
			}
			if n == 0 {
				return actions.Result{Message: "I found no matching reminder."}, nil
			}
			return actions.Result{Message: fmt.Sprintf("Cancelled %d reminder(s).", n)}, nil
		},
	})
}
