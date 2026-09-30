// Package features manages independently enabled capabilities (Telegram,
// assistant, music, ...). A disabled feature is never started, so it costs no
// resources and never touches its external service.
package features

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/juancer/nexo/internal/store"
)

// ErrUnknownFeature is returned for a feature that was never registered.
var ErrUnknownFeature = errors.New("features: unknown feature")

// Feature is a capability with a lifecycle.
//
// Start must return promptly: a feature runs its own goroutines and stops them
// in Stop. A failure in one feature never affects the others.
type Feature interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Status describes a registered feature.
type Status struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	LastError string `json:"last_error,omitempty"`
}

type entry struct {
	feature Feature
	running bool
	lastErr string
}

// Manager keeps running features in line with the flags stored in SQLite.
type Manager struct {
	mu       sync.Mutex
	store    *store.Store
	log      *slog.Logger
	features map[string]*entry
}

// NewManager returns an empty Manager.
func NewManager(s *store.Store, log *slog.Logger) *Manager {
	return &Manager{store: s, log: log, features: map[string]*entry{}}
}

// Register adds a feature. Newly registered features start disabled.
func (m *Manager) Register(ctx context.Context, f Feature) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.features[f.Name()]; dup {
		return fmt.Errorf("features: %q already registered", f.Name())
	}
	if err := m.store.EnsureFeature(ctx, f.Name()); err != nil {
		return err
	}
	m.features[f.Name()] = &entry{feature: f}
	return nil
}

// Sync starts every enabled feature and stops every disabled one.
func (m *Manager) Sync(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.store.ListFeatures(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if e, ok := m.features[row.Name]; ok {
			m.reconcile(ctx, e, row.Enabled)
		}
	}
	return nil
}

// SetEnabled persists the flag and immediately starts or stops the feature.
func (m *Manager) SetEnabled(ctx context.Context, name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.features[name]
	if !ok {
		return ErrUnknownFeature
	}
	if err := m.store.SetFeatureEnabled(ctx, name, enabled); err != nil {
		return err
	}
	m.reconcile(ctx, e, enabled)
	return nil
}

// List returns the status of all registered features, ordered by name.
func (m *Manager) List(ctx context.Context) ([]Status, error) {
	rows, err := m.store.ListFeatures(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Status{}
	for _, row := range rows {
		if e, ok := m.features[row.Name]; ok {
			out = append(out, Status{Name: row.Name, Enabled: row.Enabled, Running: e.running, LastError: e.lastErr})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Shutdown stops all running features.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.features {
		m.reconcile(ctx, e, false)
	}
}

// reconcile must be called with m.mu held.
func (m *Manager) reconcile(ctx context.Context, e *entry, enabled bool) {
	name := e.feature.Name()
	switch {
	case enabled && !e.running:
		if err := e.feature.Start(ctx); err != nil {
			e.lastErr = err.Error()
			m.log.Error("feature failed to start", "feature", name, "error", err)
			return
		}
		e.running, e.lastErr = true, ""
		m.log.Info("feature started", "feature", name)
	case !enabled && e.running:
		if err := e.feature.Stop(ctx); err != nil {
			e.lastErr = err.Error()
			m.log.Error("feature failed to stop cleanly", "feature", name, "error", err)
		}
		e.running = false
		m.log.Info("feature stopped", "feature", name)
	}
}

// Enabled reports whether a feature is switched on in SQLite. It implements
// actions.FeatureChecker.
func (m *Manager) Enabled(ctx context.Context, name string) (bool, error) {
	return m.store.FeatureEnabled(ctx, name)
}

// Restart stops and starts a feature again if it is enabled, for example after
// its credentials changed.
func (m *Manager) Restart(ctx context.Context, name string) error {
	// The flag is read with the lock held, so a switch-off that lands meanwhile is honoured.
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.features[name]
	if !ok {
		return ErrUnknownFeature
	}
	enabled, err := m.store.FeatureEnabled(ctx, name)
	if err != nil {
		return err
	}
	m.reconcile(ctx, e, false)
	m.reconcile(ctx, e, enabled)
	return nil
}
