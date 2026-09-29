package actions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/permissions"
)

type fakeFeatures map[string]bool

func (f fakeFeatures) Enabled(_ context.Context, name string) (bool, error) { return f[name], nil }

type fakePerms map[int64]map[permissions.Permission]bool

func (f fakePerms) Has(_ context.Context, id int64, p permissions.Permission) (bool, error) {
	return f[id][p], nil
}

type fakeSchedule struct{ open bool }

func (f fakeSchedule) Allowed(context.Context, string, time.Time) (bool, error) { return f.open, nil }

func newEngine(t *testing.T, feats fakeFeatures, perms fakePerms, sched ScheduleChecker) (*Engine, *int) {
	t.Helper()
	e := NewEngine(feats, perms, sched, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ran := new(int)
	min, max := Range(0, 100)
	e.MustRegister(Action{
		Name: "set_volume", Description: "volume", Exposure: ExposeDevice | ExposeAssistant | ExposeTelegram,
		Params:  []Param{{Name: "level", Type: Integer, Required: true, Min: min, Max: max}},
		Handler: func(context.Context, Call) (Result, error) { *ran++; return Result{Message: "ok"}, nil },
	})
	e.MustRegister(Action{
		Name: "send_message", Feature: "telegram", Permission: permissions.SendMessage,
		Exposure: ExposeTelegram | ExposeAssistant,
		Params:   []Param{{Name: "text", Type: String, Required: true, MaxLen: 10}},
		Handler:  func(context.Context, Call) (Result, error) { *ran++; return Result{Message: "sent"}, nil },
	})
	e.MustRegister(Action{
		Name: "emergency", Feature: "telegram", Permission: permissions.EmergencyAlert, BypassSchedule: true,
		Exposure: ExposeTelegram,
		Handler:  func(context.Context, Call) (Result, error) { *ran++; return Result{Message: "alert"}, nil },
	})
	e.MustRegister(Action{
		Name: "device_only", Exposure: ExposeDevice,
		Handler: func(context.Context, Call) (Result, error) { *ran++; return Result{}, nil },
	})
	return e, ran
}

var device = Actor{Kind: ActorDevice}

func TestUnknownAction(t *testing.T) {
	e, ran := newEngine(t, nil, nil, nil)
	if _, err := e.Execute(context.Background(), device, "rm_rf", nil); !errors.Is(err, ErrUnknownAction) || *ran != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestExecuteValid(t *testing.T) {
	e, ran := newEngine(t, nil, nil, nil)
	res, err := e.Execute(context.Background(), device, "set_volume", map[string]any{"level": float64(40)})
	if err != nil || res.Message != "ok" || *ran != 1 {
		t.Fatalf("res=%+v err=%v ran=%d", res, err, *ran)
	}
}

func TestInvalidParams(t *testing.T) {
	e, ran := newEngine(t, nil, nil, nil)
	for name, params := range map[string]map[string]any{
		"missing":       nil,
		"wrong type":    {"level": "loud"},
		"out of range":  {"level": float64(101)},
		"fractional":    {"level": 10.5},
		"unknown param": {"level": float64(5), "shell": "rm -rf /"},
	} {
		if _, err := e.Execute(context.Background(), device, "set_volume", params); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if *ran != 0 {
		t.Fatal("handler ran with invalid parameters")
	}
}

func TestStringLimits(t *testing.T) {
	e, _ := newEngine(t, fakeFeatures{"telegram": true}, nil, nil)
	assistant := Actor{Kind: ActorAssistant}
	if _, err := e.Execute(context.Background(), assistant, "send_message", map[string]any{"text": "12345678901"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.Execute(context.Background(), assistant, "send_message", map[string]any{"text": "nul\x00"}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("NUL accepted: %v", err)
	}
}

func TestExposure(t *testing.T) {
	e, ran := newEngine(t, fakeFeatures{"telegram": true}, nil, nil)
	if _, err := e.Execute(context.Background(), Actor{Kind: ActorAssistant}, "device_only", nil); !errors.Is(err, ErrNotExposed) {
		t.Fatalf("assistant reached a device-only action: %v", err)
	}
	if _, err := e.Execute(context.Background(), Actor{Kind: ActorAssistant}, "emergency", nil); !errors.Is(err, ErrNotExposed) {
		t.Fatalf("assistant reached the emergency action: %v", err)
	}
	if *ran != 0 {
		t.Fatal("handler ran")
	}
}

func TestFeatureDisabled(t *testing.T) {
	e, ran := newEngine(t, fakeFeatures{"telegram": false}, nil, nil)
	_, err := e.Execute(context.Background(), Actor{Kind: ActorAssistant}, "send_message", map[string]any{"text": "hi"})
	if !errors.Is(err, ErrFeatureDisabled) || *ran != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestPermissionRequiredForTelegramActors(t *testing.T) {
	perms := fakePerms{1: {permissions.SendMessage: true}}
	e, ran := newEngine(t, fakeFeatures{"telegram": true}, perms, nil)
	ctx := context.Background()
	if _, err := e.Execute(ctx, Actor{Kind: ActorTelegram, UserID: 2}, "send_message", map[string]any{"text": "hi"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user without permission: %v", err)
	}
	if _, err := e.Execute(ctx, Actor{Kind: ActorTelegram, UserID: 1}, "send_message", map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("user with permission: %v", err)
	}
	if _, err := e.Execute(ctx, Actor{Kind: ActorTelegram, UserID: 1}, "emergency", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("emergency without emergency_alert: %v", err)
	}
	if *ran != 1 {
		t.Fatalf("ran = %d", *ran)
	}
}

func TestScheduleBlocksButEmergencyBypasses(t *testing.T) {
	perms := fakePerms{1: {permissions.SendMessage: true, permissions.EmergencyAlert: true}}
	e, _ := newEngine(t, fakeFeatures{"telegram": true}, perms, fakeSchedule{open: false})
	ctx := context.Background()
	tg := Actor{Kind: ActorTelegram, UserID: 1}
	if _, err := e.Execute(ctx, tg, "send_message", map[string]any{"text": "hi"}); !errors.Is(err, ErrScheduleBlocked) {
		t.Fatalf("send outside schedule: %v", err)
	}
	if _, err := e.Execute(ctx, tg, "emergency", nil); err != nil {
		t.Fatalf("emergency must bypass schedules: %v", err)
	}
	if _, err := e.Execute(ctx, Actor{Kind: ActorAdmin}, "send_message", map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("admin is not schedule-limited: %v", err)
	}
}

func TestToolsForListsOnlyUsableAssistantActions(t *testing.T) {
	feats := fakeFeatures{"telegram": true}
	e, _ := newEngine(t, feats, nil, nil)
	names := func() []string {
		var n []string
		for _, tl := range e.ToolsFor(context.Background(), ActorAssistant) {
			n = append(n, tl.Name)
		}
		return n
	}
	if got := names(); len(got) != 2 || got[0] != "send_message" || got[1] != "set_volume" {
		t.Fatalf("tools = %v", got)
	}
	feats["telegram"] = false
	if got := names(); len(got) != 1 || got[0] != "set_volume" {
		t.Fatalf("tools with telegram off = %v", got)
	}
}

func TestRegisterValidation(t *testing.T) {
	e := NewEngine(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := func(context.Context, Call) (Result, error) { return Result{}, nil }
	if e.Register(Action{Name: "", Handler: h}) == nil || e.Register(Action{Name: "x"}) == nil {
		t.Fatal("invalid action accepted")
	}
	if e.Register(Action{Name: "x", Handler: h, Params: []Param{{Name: "a", Type: "weird"}}}) == nil {
		t.Fatal("bad param type accepted")
	}
	e.MustRegister(Action{Name: "x", Handler: h})
	if e.Register(Action{Name: "x", Handler: h}) == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestJSONSchema(t *testing.T) {
	min, max := Range(0, 10)
	s := JSONSchema([]Param{{Name: "n", Type: Integer, Required: true, Min: min, Max: max}, {Name: "s", Type: String, Enum: []string{"a"}}})
	props := s["properties"].(map[string]any)
	if s["additionalProperties"] != false || len(props) != 2 || s["required"].([]string)[0] != "n" {
		t.Fatalf("schema = %v", s)
	}
}
