// Package audit records important administrative actions.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

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

// maxEntries is how many audit rows are kept; older ones are pruned.
const maxEntries = 5000

// Prune deletes the oldest entries beyond maxEntries.
func (l *Logger) Prune(ctx context.Context) error {
	return l.store.PruneAudit(ctx, maxEntries)
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
