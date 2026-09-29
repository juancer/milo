package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ScheduleRule is one allowed window for a feature.
type ScheduleRule struct {
	ID          int64
	Feature     string
	DayOfWeek   int // 0 = Sunday
	StartMinute int
	EndMinute   int
	Enabled     bool
}

// ScheduleEnabled reports whether a feature's schedule is switched on.
func (s *Store) ScheduleEnabled(ctx context.Context, feature string) (bool, error) {
	var on bool
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM usage_schedules WHERE feature = ?`, feature).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: schedule enabled: %w", err)
	}
	return on, nil
}

// ListScheduleRules returns rules for one feature, or for all when feature is "".
func (s *Store) ListScheduleRules(ctx context.Context, feature string) ([]ScheduleRule, error) {
	q := `SELECT id, feature, day_of_week, start_minute, end_minute, enabled FROM usage_schedule_rules`
	var args []any
	if feature != "" {
		q += ` WHERE feature = ?`
		args = append(args, feature)
	}
	q += ` ORDER BY feature, day_of_week, start_minute`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list schedule rules: %w", err)
	}
	defer rows.Close()
	var out []ScheduleRule
	for rows.Next() {
		var r ScheduleRule
		if err := rows.Scan(&r.ID, &r.Feature, &r.DayOfWeek, &r.StartMinute, &r.EndMinute, &r.Enabled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveSchedule sets the schedule switch and replaces all rules of a feature atomically.
func (s *Store) SaveSchedule(ctx context.Context, feature string, enabled bool, rules []ScheduleRule) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_schedules (feature, enabled) VALUES (?, ?)
		ON CONFLICT (feature) DO UPDATE SET enabled = excluded.enabled`, feature, enabled); err != nil {
		return fmt.Errorf("store: save schedule: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_schedule_rules WHERE feature = ?`, feature); err != nil {
		return err
	}
	for _, r := range rules {
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_schedule_rules (feature, day_of_week, start_minute, end_minute, enabled)
			VALUES (?, ?, ?, ?, ?)`, feature, r.DayOfWeek, r.StartMinute, r.EndMinute, r.Enabled); err != nil {
			return fmt.Errorf("store: save schedule rule: %w", err)
		}
	}
	return tx.Commit()
}
