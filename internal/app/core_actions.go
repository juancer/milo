package app

import (
	"context"
	"fmt"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
)

// registerCoreActions registers actions that need no feature.
func (a *App) registerCoreActions() {
	lo, hi := actions.Range(0, 100)
	dlo, dhi := actions.Range(-100, 100)
	all := actions.ExposeDevice | actions.ExposeAssistant | actions.ExposeTelegram

	a.Actions.MustRegister(actions.Action{
		Name:        "set_volume",
		Description: "Set the speaker volume (level 0-100) or change it by an amount (change -100..100).",
		Exposure:    all,
		Params: []actions.Param{
			{Name: "level", Description: "Absolute volume, 0 to 100", Type: actions.Integer, Min: lo, Max: hi},
			{Name: "change", Description: "Relative change, for example 10 or -10", Type: actions.Integer, Min: dlo, Max: dhi},
		},
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			if c.Args.Has("level") == c.Args.Has("change") {
				return actions.Result{}, fmt.Errorf("%w: give exactly one of level or change", actions.ErrInvalidParams)
			}
			level := c.Args.Int("level")
			if c.Args.Has("change") {
				level = a.Audio.Status(ctx).Volume + c.Args.Int("change")
			}
			level = max(0, min(100, level))
			if err := a.Audio.SetVolume(ctx, level); err != nil {
				return actions.Result{}, err
			}
			if err := a.Store.SetSetting(ctx, "audio.volume", fmt.Sprint(level)); err != nil {
				return actions.Result{}, err
			}
			a.Audio.PlayNotification(ctx, audio.NotifyOK, 100)
			return actions.Result{Message: fmt.Sprintf("Volume %d percent", level), Data: map[string]any{"volume": level}}, nil
		},
	})

	a.Actions.MustRegister(actions.Action{
		Name:        "get_device_status",
		Description: "Report whether the device is healthy: volume and which features are on.",
		Exposure:    all,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			st := a.Audio.Status(ctx)
			feats, err := a.Features.List(ctx)
			if err != nil {
				return actions.Result{}, err
			}
			on := []string{}
			for _, f := range feats {
				if f.Running {
					on = append(on, f.Name)
				}
			}
			return actions.Result{
				Message: fmt.Sprintf("Volume %d percent. %d features running.", st.Volume, len(on)),
				Data:    map[string]any{"volume": st.Volume, "features_running": on, "audio_available": st.Available},
			}, nil
		},
	})

	a.Actions.MustRegister(actions.Action{
		Name:        "stop_audio",
		Description: "Stop whatever sound is playing.",
		Exposure:    actions.ExposeDevice | actions.ExposeAssistant,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			if a.Music != nil {
				a.Music.Halt() // "stop" also ends the music for good
			}
			a.Audio.Stop()
			return actions.Result{Message: "Stopped"}, nil
		},
	})

	a.Actions.MustRegister(actions.Action{
		Name:        "get_time",
		Description: "Tell the current day and time.",
		Exposure:    all,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			now := time.Now().In(a.Schedules.Location(ctx))
			return actions.Result{Message: "It is " + now.Format("Monday, 15:04") + ".", Data: map[string]any{"time": now.Format(time.RFC3339)}}, nil
		},
	})
}
