// Package audit records important administrative actions.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
)

// Logger writes audit entries to SQLite.
type Logger struct {
	store *store.Store
	log   *slog.Logger
}

// New returns a Logger.
func New(s *store.Store, log *slog.Logger) *Logger { return &Logger{store: s, log: log} }

const (
	// retention is how long audit rows are kept.
	retention = 90 * 24 * time.Hour
	// maxEntries is a hard safety cap; beyond it the heaviest actor's oldest
	// rows are dropped first.
	maxEntries = 50000
)

// Prune deletes entries older than the retention period, then enforces the cap.
func (l *Logger) Prune(ctx context.Context) error {
	return l.store.PruneAudit(ctx, retention, maxEntries)
}

// Record appends an entry. Detail values under credential-like keys are
// redacted, so callers cannot leak a secret into the audit trail by accident.
// A failure to write is logged and returned; callers decide whether it matters.
func (l *Logger) Record(ctx context.Context, actor, action, target string, details map[string]string) error {
	safe := make(map[string]string, len(details))
	for k, v := range details {
		if system.IsSensitiveKey(k) {
			v = "[redacted]"
		}
		safe[k] = v
	}
	encoded := ""
	if len(safe) > 0 {
		b, err := json.Marshal(safe)
		if err != nil {
			return err
		}
		encoded = string(b)
	}
	err := l.store.InsertAudit(ctx, store.AuditEntry{Actor: actor, Action: action, Target: target, Details: encoded})
	if err != nil {
		l.log.Error("audit write failed", "action", action, "error", err)
	}
	return err
}
