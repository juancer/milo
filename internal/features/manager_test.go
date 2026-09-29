package features

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/juancer/nexo/internal/store"
)

type fake struct {
	name          string
	startErr      error
	starts, stops int
}

func (f *fake) Name() string                { return f.name }
func (f *fake) Start(context.Context) error { f.starts++; return f.startErr }
func (f *fake) Stop(context.Context) error  { f.stops++; return nil }

func newManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewManager(st, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func TestDisabledFeatureIsNeverStarted(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t)
	tg := &fake{name: "telegram"}
	if err := m.Register(ctx, tg); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if tg.starts != 0 {
		t.Fatal("disabled feature was started")
	}
}

func TestEnableStartsAndDisableStops(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t)
	tg := &fake{name: "telegram"}
	m.Register(ctx, tg)

	if err := m.SetEnabled(ctx, "telegram", true); err != nil {
		t.Fatal(err)
	}
	if tg.starts != 1 {
		t.Fatalf("starts = %d", tg.starts)
	}
	m.SetEnabled(ctx, "telegram", true) // idempotent
	if tg.starts != 1 {
		t.Fatal("started twice")
	}
	m.SetEnabled(ctx, "telegram", false)
	if tg.stops != 1 {
		t.Fatalf("stops = %d", tg.stops)
	}
}

func TestEnabledFlagSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	m1, st := newManager(t)
	m1.Register(ctx, &fake{name: "assistant"})
	m1.SetEnabled(ctx, "assistant", true)

	m2 := NewManager(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	again := &fake{name: "assistant"}
	m2.Register(ctx, again)
	m2.Sync(ctx)
	if again.starts != 1 {
		t.Fatal("feature enabled in SQLite did not start after restart")
	}
}

func TestFailingFeatureDoesNotBlockOthers(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t)
	bad := &fake{name: "a-bad", startErr: errors.New("boom")}
	good := &fake{name: "b-good"}
	m.Register(ctx, bad)
	m.Register(ctx, good)
	m.SetEnabled(ctx, "a-bad", true)
	m.SetEnabled(ctx, "b-good", true)

	list, _ := m.List(ctx)
	if len(list) != 2 || list[0].Running || list[0].LastError != "boom" || !list[1].Running {
		t.Fatalf("List = %+v", list)
	}
}

func TestUnknownAndDuplicate(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t)
	if err := m.SetEnabled(ctx, "ghost", true); !errors.Is(err, ErrUnknownFeature) {
		t.Fatalf("err = %v", err)
	}
	m.Register(ctx, &fake{name: "x"})
	if err := m.Register(ctx, &fake{name: "x"}); err == nil {
		t.Fatal("duplicate registration accepted")
	}
}

func TestShutdownStopsRunningFeatures(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t)
	f := &fake{name: "x"}
	m.Register(ctx, f)
	m.SetEnabled(ctx, "x", true)
	m.Shutdown(ctx)
	if f.stops != 1 {
		t.Fatalf("stops = %d", f.stops)
	}
}
