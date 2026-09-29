package store

import (
	"context"
	"fmt"
	"time"
)

// WakeTemplate is one enrolled recording of the wake phrase.
type WakeTemplate struct {
	ID        int64
	Features  []byte
	Frames    int
	CreatedAt time.Time
}

// AddWakeTemplate stores a template.
func (s *Store) AddWakeTemplate(ctx context.Context, features []byte, frames int) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO wake_templates (features, frames) VALUES (?, ?)`, features, frames)
	if err != nil {
		return 0, fmt.Errorf("store: add wake template: %w", err)
	}
	return res.LastInsertId()
}

// ListWakeTemplates returns all templates, oldest first.
func (s *Store) ListWakeTemplates(ctx context.Context) ([]WakeTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, features, frames, created_at FROM wake_templates ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list wake templates: %w", err)
	}
	defer rows.Close()
	var out []WakeTemplate
	for rows.Next() {
		var t WakeTemplate
		var at string
		if err := rows.Scan(&t.ID, &t.Features, &t.Frames, &at); err != nil {
			return nil, err
		}
		t.CreatedAt = parseTime(at)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteWakeTemplate removes one template.
func (s *Store) DeleteWakeTemplate(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM wake_templates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
