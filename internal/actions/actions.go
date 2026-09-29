// Package actions is Nexo's security boundary.
//
// Buttons, Telegram commands, voice and LLM tool calls are all reduced to the
// same thing: "actor X asks to run action Y with parameters Z". The Engine
// decides whether that is allowed and runs it. Nothing else may reach the
// operating system on behalf of an external input, and there is deliberately
// no action that executes commands, reads arbitrary files or edits parental
// configuration.
package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/juancer/nexo/internal/permissions"
)

// ActorKind says where a request came from.
type ActorKind string

const (
	ActorDevice    ActorKind = "device"    // the child at the device (buttons, wake word)
	ActorTelegram  ActorKind = "telegram"  // an authorized Telegram user
	ActorAssistant ActorKind = "assistant" // the LLM, acting for the device
	ActorAdmin     ActorKind = "admin"     // the authenticated web admin
)

// Actor is who asks for an action.
type Actor struct {
	Kind   ActorKind
	UserID int64  // Telegram/admin user id when known
	Label  string // for logs and audit; never a secret
}

// Exposure lists which kinds of actor may invoke an action.
type Exposure uint8

const (
	ExposeDevice Exposure = 1 << iota
	ExposeAssistant
	ExposeTelegram
)

func (e Exposure) allows(k ActorKind) bool {
	switch k {
	case ActorDevice:
		return e&ExposeDevice != 0
	case ActorAssistant:
		return e&ExposeAssistant != 0
	case ActorTelegram:
		return e&ExposeTelegram != 0
	case ActorAdmin:
		return true
	}
	return false
}

// Args are validated action parameters.
type Args map[string]any

// String returns a string parameter ("" if absent).
func (a Args) String(name string) string { s, _ := a[name].(string); return s }

// Bytes returns a binary parameter.
func (a Args) Bytes(name string) []byte { b, _ := a[name].([]byte); return b }

// Int returns an integer parameter (0 if absent).
func (a Args) Int(name string) int { n, _ := a[name].(int); return n }

// Bool returns a boolean parameter.
func (a Args) Bool(name string) bool { b, _ := a[name].(bool); return b }

// Has reports whether a parameter was supplied.
func (a Args) Has(name string) bool { _, ok := a[name]; return ok }

// Call is a validated request handed to an action's handler.
type Call struct {
	Actor Actor
	Args  Args
}

// Result is what an action reports back. Message is safe to speak or send.
type Result struct {
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// Action is a registered capability.
type Action struct {
	Name        string
	Description string
	Params      []Param
	// Feature is the feature that must be enabled ("" for core actions).
	Feature string
	// Permission a Telegram or admin user must hold ("" for none).
	Permission permissions.Permission
	Exposure   Exposure
	// BypassSchedule lets the action run outside usage schedules (emergency).
	BypassSchedule bool
	Handler        func(ctx context.Context, c Call) (Result, error)
}

// Errors returned by Engine.Execute. Callers may test them with errors.Is.
var (
	ErrUnknownAction   = errors.New("unknown action")
	ErrNotExposed      = errors.New("action is not available to this actor")
	ErrFeatureDisabled = errors.New("feature is disabled")
	ErrForbidden       = errors.New("permission denied")
	ErrScheduleBlocked = errors.New("not allowed at this time")
	ErrInvalidParams   = errors.New("invalid parameters")
)

// FeatureChecker reports whether a feature is enabled.
type FeatureChecker interface {
	Enabled(ctx context.Context, feature string) (bool, error)
}

// PermissionChecker reports whether a user holds a permission.
type PermissionChecker interface {
	Has(ctx context.Context, userID int64, p permissions.Permission) (bool, error)
}

// ScheduleChecker reports whether a feature may be used at a given moment.
type ScheduleChecker interface {
	Allowed(ctx context.Context, feature string, at time.Time) (bool, error)
}

// Engine validates and runs actions.
type Engine struct {
	mu        sync.RWMutex
	actions   map[string]Action
	features  FeatureChecker
	perms     PermissionChecker
	schedules ScheduleChecker // may be nil: no schedules configured
	log       *slog.Logger
	now       func() time.Time
}

// NewEngine builds an Engine. schedules may be nil.
func NewEngine(f FeatureChecker, p PermissionChecker, s ScheduleChecker, log *slog.Logger) *Engine {
	return &Engine{actions: map[string]Action{}, features: f, perms: p, schedules: s, log: log, now: time.Now}
}

// SetSchedules installs the schedule checker (it is created after the engine).
func (e *Engine) SetSchedules(s ScheduleChecker) {
	e.mu.Lock()
	e.schedules = s
	e.mu.Unlock()
}

// Register adds an action. Names must be unique and handlers non-nil.
func (e *Engine) Register(a Action) error {
	if a.Name == "" || a.Handler == nil {
		return errors.New("actions: name and handler are required")
	}
	if err := validateSchema(a.Params); err != nil {
		return fmt.Errorf("actions: %s: %w", a.Name, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, dup := e.actions[a.Name]; dup {
		return fmt.Errorf("actions: %q already registered", a.Name)
	}
	e.actions[a.Name] = a
	return nil
}

// MustRegister is Register for start-up wiring; it panics on programmer error.
func (e *Engine) MustRegister(a Action) {
	if err := e.Register(a); err != nil {
		panic(err)
	}
}

// Has reports whether an action with this name is registered.
func (e *Engine) Has(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.actions[name]
	return ok
}

// Execute runs the full authorisation pipeline and then the action:
// exists -> exposed to actor -> permission -> feature -> schedule -> parameters -> run.
func (e *Engine) Execute(ctx context.Context, actor Actor, name string, raw map[string]any) (Result, error) {
	e.mu.RLock()
	a, ok := e.actions[name]
	sched := e.schedules
	e.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownAction, name)
	}
	if !a.Exposure.allows(actor.Kind) {
		return Result{}, fmt.Errorf("%w: %s", ErrNotExposed, name)
	}
	if a.Permission != "" && actor.Kind == ActorTelegram {
		ok, err := e.perms.Has(ctx, actor.UserID, a.Permission)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			e.log.Warn("action denied", "action", name, "actor", actor.Kind, "reason", "permission")
			return Result{}, fmt.Errorf("%w: %s", ErrForbidden, name)
		}
	}
	if a.Feature != "" {
		on, err := e.features.Enabled(ctx, a.Feature)
		if err != nil {
			return Result{}, err
		}
		if !on {
			return Result{}, fmt.Errorf("%w: %s", ErrFeatureDisabled, a.Feature)
		}
		if sched != nil && !a.BypassSchedule && actor.Kind != ActorAdmin {
			allowed, err := sched.Allowed(ctx, a.Feature, e.now())
			if err != nil {
				return Result{}, err
			}
			if !allowed {
				return Result{}, fmt.Errorf("%w: %s", ErrScheduleBlocked, a.Feature)
			}
		}
	}
	args, err := validateArgs(a.Params, raw)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s: %v", ErrInvalidParams, name, err)
	}
	e.log.Debug("action", "name", name, "actor", actor.Kind)
	return a.Handler(ctx, Call{Actor: actor, Args: args})
}

// Tool describes an action to an LLM.
type Tool struct {
	Name        string
	Description string
	Params      []Param
}

// ToolsFor lists the actions a kind of actor may call and that are currently
// usable (feature enabled, schedule open). Used to build the LLM tool list.
func (e *Engine) ToolsFor(ctx context.Context, kind ActorKind) []Tool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Tool
	for _, a := range e.actions {
		if !a.Exposure.allows(kind) {
			continue
		}
		if a.Feature != "" {
			if on, err := e.features.Enabled(ctx, a.Feature); err != nil || !on {
				continue
			}
			if e.schedules != nil && !a.BypassSchedule {
				if ok, err := e.schedules.Allowed(ctx, a.Feature, e.now()); err != nil || !ok {
					continue
				}
			}
		}
		out = append(out, Tool{Name: a.Name, Description: a.Description, Params: a.Params})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Info describes a registered action for the admin UI.
type Info struct {
	Name        string
	Description string
	Feature     string
	Exposure    Exposure
	Params      []Param
}

// List returns all registered actions ordered by name.
func (e *Engine) List() []Info {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Info, 0, len(e.actions))
	for _, a := range e.actions {
		out = append(out, Info{a.Name, a.Description, a.Feature, a.Exposure, a.Params})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Allows reports whether the exposure includes the given actor kind.
func (e Exposure) Allows(k ActorKind) bool { return e.allows(k) }
