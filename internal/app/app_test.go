package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/secrets"
	"github.com/juancer/milo/internal/store"
	"github.com/juancer/milo/internal/system"
)

func newApp(t *testing.T) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vault, _ := secrets.New(make([]byte, 32), st)
	a, err := New(ctx, system.Config{Hardware: "mock", DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)), st, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSetVolumeActionAbsoluteRelativeAndClamped(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	dev := actions.Actor{Kind: actions.ActorDevice}

	if _, err := a.Actions.Execute(ctx, dev, "set_volume", map[string]any{"level": float64(40)}); err != nil {
		t.Fatal(err)
	}
	if v := a.MockAudio.Status(ctx).Volume; v != 40 {
		t.Fatalf("volume = %d", v)
	}
	a.Actions.Execute(ctx, dev, "set_volume", map[string]any{"change": float64(-100)})
	if v := a.MockAudio.Status(ctx).Volume; v != 0 {
		t.Fatalf("clamped volume = %d", v)
	}
	if got := a.Setting(ctx, "audio.volume", ""); got != "0" {
		t.Fatalf("volume not persisted: %q", got)
	}
	for _, bad := range []map[string]any{nil, {"level": float64(1), "change": float64(1)}} {
		if _, err := a.Actions.Execute(ctx, dev, "set_volume", bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestButtonPressRunsMappedActionThroughEngine(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	buttons, _ := a.Store.ListButtons(ctx)
	red := buttons[0]
	red.Actions = map[string]store.ButtonAction{"short_press": {Action: "set_volume", Parameters: `{"level":33}`}}
	if err := a.Store.UpdateButton(ctx, red); err != nil {
		t.Fatal(err)
	}
	if err := a.Buttons.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	a.MockGPIO.Tap(red.GPIO)
	eventually(t, "volume 33", func() bool { return a.MockAudio.Status(ctx).Volume == 33 })
}

func TestSameCodeReconfiguredButton(t *testing.T) {
	// Device B: the "Green" button is reconfigured to volume up; code is unchanged.
	a := newApp(t)
	ctx := context.Background()
	buttons, _ := a.Store.ListButtons(ctx)
	green := buttons[2]
	green.Actions = map[string]store.ButtonAction{"short_press": {Action: "set_volume", Parameters: `{"change":5}`}}
	a.Store.UpdateButton(ctx, green)
	a.Buttons.Reload(ctx)
	before := a.MockAudio.Status(ctx).Volume
	a.MockGPIO.Tap(green.GPIO)
	eventually(t, "volume up", func() bool { return a.MockAudio.Status(ctx).Volume == before+5 })
}

func TestUnknownButtonActionPlaysErrorTone(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	buttons, _ := a.Store.ListButtons(ctx)
	a.MockGPIO.Tap(buttons[0].GPIO) // default mapping: "talk", not registered in this phase
	eventually(t, "error tone", func() bool {
		for _, p := range a.MockAudio.Played() {
			if p.Label == "error" {
				return true
			}
		}
		return false
	})
}

func TestVolumeRestoredFromSettingsAtStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	defer st.Close()
	st.SetSetting(ctx, "audio.volume", "25")
	vault, _ := secrets.New(make([]byte, 32), st)
	a, err := New(ctx, system.Config{Hardware: "mock"}, slog.New(slog.NewTextHandler(io.Discard, nil)), st, vault)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if v := a.MockAudio.Status(ctx).Volume; v != 25 {
		t.Fatalf("volume = %d, want 25", v)
	}
}
