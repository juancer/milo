package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/juancer/nexo/internal/app"
	"github.com/juancer/nexo/internal/audio"
	"github.com/juancer/nexo/internal/buttons"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
)

// openApp builds the application for a one-off hardware check.
func openApp(ctx context.Context, cfg system.Config) (*app.App, func(), error) {
	log, err := system.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	key, err := secrets.LoadKey(cfg.KeyPath)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	vault, err := secrets.New(key, st)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	a, err := app.New(ctx, cfg, log, st, vault)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return a, func() { a.Close(); st.Close() }, nil
}

// testAudio plays the built-in sounds, then records 3 seconds and plays it back.
func testAudio(ctx context.Context, cfg system.Config) error {
	a, done, err := openApp(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()

	st := a.Audio.Status(ctx)
	fmt.Printf("audio backend: %s, available: %v, volume: %d%% %s\n", st.Backend, st.Available, st.Volume, st.Detail)
	for _, n := range []audio.Notification{audio.NotifyMessage, audio.NotifyEmergency, audio.NotifyOK} {
		fmt.Printf("playing %q sound...\n", n)
		if err := a.Audio.PlayNotification(ctx, n, 100); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Println("recording for 3 seconds - say something...")
	a.Audio.PlayNotification(ctx, audio.NotifyRecordStart, 100)
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	wav, err := a.Audio.Record(rctx, audio.RecordOptions{MaxDuration: 3 * time.Second})
	if err != nil {
		return err
	}
	fmt.Printf("recorded %s, playing it back...\n", audio.Duration(wav).Round(100*time.Millisecond))
	if err := a.Audio.Play(ctx, wav); err != nil {
		return err
	}
	fmt.Println("audio test finished")
	return nil
}

// testButtons prints every recognised press until interrupted. It uses the
// button mapping stored in SQLite but never runs the mapped actions.
func testButtons(ctx context.Context, cfg system.Config) error {
	if cfg.Hardware == "mock" {
		return fmt.Errorf("test-buttons needs real GPIO; NEXO_HARDWARE is mock (use the simulator in the web admin instead)")
	}
	a, done, err := openApp(ctx, cfg)
	if err != nil {
		return err
	}
	defer done()
	if a.Buttons == nil {
		return fmt.Errorf("GPIO unavailable: %v", a.HardwareErrors)
	}
	// Replace the dispatcher's effect: print instead of executing.
	a.Buttons.SetDispatcher(func(_ context.Context, e buttons.Event) {
		fmt.Printf("%s  %-8s %-12s -> %s %v\n", time.Now().Format("15:04:05"), e.ButtonName, e.Type, e.Action, e.Params)
	})
	btns, _ := a.Store.ListButtons(ctx)
	fmt.Println("Press the buttons (Ctrl-C to quit). Configured:")
	for _, b := range btns {
		fmt.Printf("  %-8s GPIO%-2d enabled=%v\n", b.Name, b.GPIO, b.Enabled)
	}
	if err := a.Buttons.Reload(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
