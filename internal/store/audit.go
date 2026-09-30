package store

import (
	"context"
	"fmt"
	"time"
)

// AuditEntry is one row of the audit log.
type AuditEntry struct {
	ID      int64
	At      time.Time
	Actor   string
	Action  string
	Target  string
	Details string
}

// InsertAudit appends an entry to the audit log.
func (s *Store) InsertAudit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, target, details) VALUES (?, ?, ?, ?)`,
		e.Actor, e.Action, e.Target, e.Details)
	if err != nil {
		return fmt.Errorf("store: insert audit: %w", err)
	}
	return nil
}

// PruneAudit deletes all but the newest keep entries.
func (s *Store) PruneAudit(ctx context.Context, keep int) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM audit_log WHERE id <= (SELECT COALESCE(MAX(id), 0) FROM audit_log) - ?`, keep)
	if err != nil {
		return fmt.Errorf("store: prune audit: %w", err)
	}
	return nil
}

// ListAudit returns the most recent entries, newest first.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, actor, action, target, details FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Target, &e.Details); err != nil {
			return nil, fmt.Errorf("store: scan audit: %w", err)
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
