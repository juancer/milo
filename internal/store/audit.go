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
	Repeats int // occurrences folded into this row (1 = once)
}

// auditCoalesceWindow is how long an identical event is folded into the
// existing row instead of being inserted again.
const auditCoalesceWindow = 60 * time.Second

// InsertAudit appends an entry to the audit log. When the same actor, action,
// target and details were already recorded within auditCoalesceWindow, the
// existing row's repeat counter is incremented instead.
func (s *Store) InsertAudit(ctx context.Context, e AuditEntry) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE audit_log SET repeats = repeats + 1
		 WHERE id = (SELECT MAX(id) FROM audit_log
		             WHERE actor = ? AND action = ? AND target = ? AND details = ?
		               AND at >= strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		e.Actor, e.Action, e.Target, e.Details, fmt.Sprintf("-%d seconds", int(auditCoalesceWindow.Seconds())))
	if err != nil {
		return fmt.Errorf("store: coalesce audit: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO audit_log (actor, action, target, details) VALUES (?, ?, ?, ?)`,
		e.Actor, e.Action, e.Target, e.Details)
	if err != nil {
		return fmt.Errorf("store: insert audit: %w", err)
	}
	return nil
}

// PruneAudit deletes entries older than maxAge, then, if more than maxRows
// remain, deletes the oldest rows of whichever actor has the most rows until
// the cap is met, so one actor's volume cannot evict another actor's history.
func (s *Store) PruneAudit(ctx context.Context, maxAge time.Duration, maxRows int) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM audit_log WHERE at < strftime('%Y-%m-%dT%H:%M:%fZ','now', ?)`,
		fmt.Sprintf("-%d seconds", int(maxAge.Seconds())))
	if err != nil {
		return fmt.Errorf("store: prune audit: %w", err)
	}
	for {
		var total int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&total); err != nil {
			return fmt.Errorf("store: prune audit: %w", err)
		}
		if total <= maxRows {
			return nil
		}
		var actor string
		var have int
		err := s.db.QueryRowContext(ctx,
			`SELECT actor, COUNT(*) AS n FROM audit_log GROUP BY actor ORDER BY n DESC, actor LIMIT 1`).Scan(&actor, &have)
		if err != nil {
			return fmt.Errorf("store: prune audit: %w", err)
		}
		drop := min(total-maxRows, have)
		_, err = s.db.ExecContext(ctx,
			`DELETE FROM audit_log WHERE id IN (SELECT id FROM audit_log WHERE actor = ? ORDER BY id LIMIT ?)`,
			actor, drop)
		if err != nil {
			return fmt.Errorf("store: prune audit: %w", err)
		}
	}
}

// ListAudit returns the most recent entries, newest first.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, actor, action, target, details, repeats FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Target, &e.Details, &e.Repeats); err != nil {
			return nil, fmt.Errorf("store: scan audit: %w", err)
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
