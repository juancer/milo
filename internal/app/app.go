// Package app wires Nexo together: it builds the store, hardware, engines and
// features and owns their lifecycle. Everything here is assembly; behaviour
// lives in the packages it connects.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/audio"
	"github.com/juancer/nexo/internal/audit"
	"github.com/juancer/nexo/internal/buttons"
	"github.com/juancer/nexo/internal/features"
	"github.com/juancer/nexo/internal/gpio"
	"github.com/juancer/nexo/internal/notifications"
	"github.com/juancer/nexo/internal/permissions"
	"github.com/juancer/nexo/internal/schedules"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
	"github.com/juancer/nexo/internal/telegram"
)

// App is the assembled application.
type App struct {
	Cfg      system.Config
	Log      *slog.Logger
	Store    *store.Store
	Vault    *secrets.Vault
	Audit    *audit.Logger
	Perms    *permissions.Checker
	Features *features.Manager
	Actions  *actions.Engine

	Audio     audio.Audio
	GPIO      gpio.Controller
	Buttons   *buttons.Engine
	Notifier  *notifications.Notifier
	Telegram  *telegram.Service
	Schedules *schedules.Service

	// Set only when NEXO_HARDWARE=mock, for tests and the simulator.
	MockAudio *audio.Mock
	MockGPIO  *gpio.Mock

	// HardwareErrors lists hardware that failed to initialise. The admin UI stays
	// available so a parent can see why.
	HardwareErrors []string

	cancel context.CancelFunc
}

// New builds the application. The store must already be open; the caller
// closes it. Hardware failures are recorded, not fatal.
func New(ctx context.Context, cfg system.Config, log *slog.Logger, st *store.Store, vault *secrets.Vault) (*App, error) {
	a := &App{Cfg: cfg, Log: log, Store: st, Vault: vault}
	a.Audit = audit.New(st, log)
	a.Perms = permissions.NewChecker(st)
	a.Features = features.NewManager(st, log)
	a.Actions = actions.NewEngine(a.Features, a.Perms, nil, log)

	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	if err := a.initHardware(runCtx); err != nil {
		cancel()
		return nil, err
	}
	a.Schedules = schedules.New(st)
	a.Actions.SetSchedules(a.Schedules)
	a.registerCoreActions()
	if err := a.initTelegram(runCtx); err != nil {
		cancel()
		return nil, err
	}

	if err := a.Features.Sync(runCtx); err != nil {
		cancel()
		return nil, err
	}
	return a, nil
}

// Close stops everything the App started.
func (a *App) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.Buttons != nil {
		a.Buttons.Stop()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.Features.Shutdown(stopCtx)
	if a.GPIO != nil {
		a.GPIO.Close()
	}
}

// Setting returns a setting or its default.
func (a *App) Setting(ctx context.Context, key, def string) string {
	v, err := a.Store.GetSetting(ctx, key)
	if err != nil {
		return def
	}
	return v
}

// IntSetting returns an integer setting or its default.
func (a *App) IntSetting(ctx context.Context, key string, def int) int {
	n, err := strconv.Atoi(a.Setting(ctx, key, ""))
	if err != nil {
		return def
	}
	return n
}

func (a *App) initHardware(ctx context.Context) error {
	if a.Cfg.Hardware == "mock" {
		a.MockGPIO, a.MockAudio = gpio.NewMock(), audio.NewMock()
		a.GPIO, a.Audio = a.MockGPIO, a.MockAudio
	} else {
		cfg := audio.ALSAConfig{
			CaptureDevice:  a.Setting(ctx, "audio.capture_device", audio.DefaultALSAConfig.CaptureDevice),
			PlaybackDevice: a.Setting(ctx, "audio.playback_device", audio.DefaultALSAConfig.PlaybackDevice),
			MixerCard:      a.Setting(ctx, "audio.mixer_card", audio.DefaultALSAConfig.MixerCard),
			MixerControl:   a.Setting(ctx, "audio.mixer_control", audio.DefaultALSAConfig.MixerControl),
		}
		alsa, err := audio.NewALSA(cfg)
		if err != nil {
			return fmt.Errorf("audio settings: %w", err)
		}
		a.Audio = alsa
		g, err := gpio.OpenChip(a.Setting(ctx, "gpio.chip", "gpiochip0"))
		if err != nil {
			a.hardwareError("buttons unavailable: %v", err)
		} else {
			a.GPIO = g
		}
	}

	if vol := a.IntSetting(ctx, "audio.volume", 70); a.Audio != nil {
		if err := a.Audio.SetVolume(ctx, vol); err != nil {
			a.hardwareError("cannot set volume: %v", err)
		}
	}

	if a.GPIO == nil {
		return nil
	}
	opts := buttons.DefaultOptions
	opts.LongPress = time.Duration(a.IntSetting(ctx, "buttons.long_press_ms", int(opts.LongPress/time.Millisecond))) * time.Millisecond
	opts.DoubleWindow = time.Duration(a.IntSetting(ctx, "buttons.double_press_ms", int(opts.DoubleWindow/time.Millisecond))) * time.Millisecond
	a.Buttons = buttons.New(a.GPIO, a.Store, a.dispatchButton, opts, a.Log)
	if err := a.Buttons.Start(ctx); err != nil {
		a.hardwareError("some buttons cannot be watched: %v", err)
	}
	return nil
}

func (a *App) hardwareError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	a.Log.Error("hardware problem", "detail", msg)
	a.HardwareErrors = append(a.HardwareErrors, msg)
}

// dispatchButton hands a recognised button event to the Action Engine.
// Buttons carry no logic of their own.
func (a *App) dispatchButton(ctx context.Context, e buttons.Event) {
	actor := actions.Actor{Kind: actions.ActorDevice, Label: "button:" + e.ButtonName}
	res, err := a.Actions.Execute(ctx, actor, e.Action, e.Params)
	if err != nil {
		level := slog.LevelWarn
		if errors.Is(err, actions.ErrUnknownAction) {
			level = slog.LevelInfo
		}
		a.Log.Log(ctx, level, "button action failed", "button", e.ButtonName, "event", e.Type, "action", e.Action, "error", err)
		a.Audio.PlayNotification(ctx, audio.NotifyError, 100)
		return
	}
	a.Log.Debug("button action done", "button", e.ButtonName, "action", e.Action, "message", res.Message)
}

func (a *App) initTelegram(ctx context.Context) error {
	a.Notifier = notifications.New(a.Audio, a.Store)
	var codec audio.Transcoder = audio.Opus{}
	if a.Cfg.Hardware == "mock" {
		codec = audio.PassThrough{} // simulated voice notes are plain WAV
	}
	a.Telegram = telegram.New(telegram.Deps{
		Store: a.Store, Vault: a.Vault, Audio: a.Audio, Codec: codec, Notify: a.Notifier,
		Actions: a.Actions, Perms: a.Perms, Audit: a.Audit, Log: a.Log, BaseURL: a.Cfg.TelegramAPIURL,
	})
	a.Telegram.SetSchedules(a.Schedules)
	a.Telegram.RegisterActions(a.Actions)
	return a.Features.Register(ctx, a.Telegram)
}
