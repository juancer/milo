package app

import (
	"context"
	"errors"

	"github.com/juancer/nexo/internal/audio"
	"github.com/juancer/nexo/internal/moderation"
	"github.com/juancer/nexo/internal/skills"
	"github.com/juancer/nexo/internal/store"
)

// initSkills builds the optional skills and registers their actions and features.
func (a *App) initSkills(ctx context.Context) error {
	common := skills.Common{
		Store: a.Store, Announce: a, Log: a.Log,
		Location:  a.Schedules.Location,
		AllowHTTP: a.Cfg.Hardware == "mock",
		Check:     a.checkText,
	}
	a.Timers = &skills.Timers{Common: common}
	a.Reminders = &skills.Reminders{Common: common}
	a.Music = &skills.Music{Common: common, Audio: a.Audio,
		// Music only resumes when there is no speech, alert or conversation going on.
		Idle: func() bool { return !a.Audio.Busy() && !a.Assistant.Busy() }}
	a.Weather = &skills.Weather{Common: common}
	a.Calendar = &skills.Calendar{Common: common, Vault: a.Vault}

	a.Timers.RegisterActions(a.Actions)
	a.Reminders.RegisterActions(a.Actions)
	a.Music.RegisterActions(a.Actions)
	a.Weather.RegisterActions(a.Actions)
	a.Calendar.RegisterActions(a.Actions)
	for _, f := range []interface {
		Name() string
		Start(context.Context) error
		Stop(context.Context) error
	}{a.Timers, a.Reminders, a.Music, a.Weather, a.Calendar} {
		if err := a.Features.Register(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// Announce makes the device sound an alert and, when text-to-speech is
// available, say text. Skills use it for timers and reminders. Music yields to it.
func (a *App) Announce(ctx context.Context, text string, alarm bool) {
	kind := audio.NotifyMessage
	if alarm {
		kind = audio.NotifyAlarm
	}
	a.Notifier.Notify(ctx, kind) //nolint:errcheck
	if text == "" {
		return
	}
	if err := a.Assistant.Speak(ctx, text); err != nil {
		a.Log.Debug("announcement not spoken", "reason", err.Error())
	}
}

// CheckText reports an error if text contains a phrase the parents blocked.
func (a *App) CheckText(ctx context.Context, text string) error { return a.checkText(ctx, text) }

var errBlockedText = errors.New("that text is not allowed")

// checkText applies the parents' blocked phrases to text that will be spoken
// to the child (timer labels, reminders).
func (a *App) checkText(ctx context.Context, text string) error {
	rules, err := a.Store.ListModerationRules(ctx)
	if err != nil {
		return err
	}
	if moderation.New(toModerationRules(rules)).Check(text, moderation.Output).Blocked {
		return errBlockedText
	}
	return nil
}

func toModerationRules(in []store.ModerationRule) []moderation.Rule {
	out := make([]moderation.Rule, 0, len(in))
	for _, r := range in {
		out = append(out, moderation.Rule{ID: r.ID, Pattern: r.Pattern, AppliesTo: r.AppliesTo, Enabled: r.Enabled})
	}
	return out
}
