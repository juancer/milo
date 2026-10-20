package app

import (
	"context"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
)

// registerListeningActions adds "no escuches" and its way back. Turning listening off is offered to the
// assistant too ("Hey Milo, no escuches"); turning it on is only for the device, because while the wake
// word is off nobody can ask the assistant: a button gives it back.
func (a *App) registerListeningActions() {
	set := func(on bool) func(ctx context.Context, c actions.Call) (actions.Result, error) {
		return func(ctx context.Context, c actions.Call) (actions.Result, error) {
			if err := a.Wake.SetMuted(ctx, on); err != nil {
				return actions.Result{}, err
			}
			tone := audio.NotifyOK
			if on {
				tone = audio.NotifyRecordStop
			}
			a.Audio.PlayNotification(ctx, tone, 100) //nolint:errcheck // a missing tone is not worth failing for
			if on {
				return actions.Result{Message: "Milo stopped listening for the wake word."}, nil
			}
			return actions.Result{Message: "Milo listens for the wake word again."}, nil
		}
	}
	a.Actions.MustRegister(actions.Action{
		Name:        "listening_off",
		Description: "Stop listening for the wake word until a button is pressed.",
		Exposure:    actions.ExposeDevice | actions.ExposeAssistant,
		Handler:     set(true),
	})
	a.Actions.MustRegister(actions.Action{
		Name:        "listening_on",
		Description: "Listen for the wake word again.",
		Exposure:    actions.ExposeDevice,
		Handler:     set(false),
	})
	a.Actions.MustRegister(actions.Action{
		Name:        "listening_toggle",
		Description: "Stop or resume listening for the wake word (for a button).",
		Exposure:    actions.ExposeDevice,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			return set(!a.Wake.Muted())(ctx, c)
		},
	})
}
