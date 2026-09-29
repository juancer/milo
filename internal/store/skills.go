package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Timer is a countdown.
type Timer struct {
	ID      int64
	Label   string
	FiresAt time.Time
}

// CreateTimer adds a pending timer.
func (s *Store) CreateTimer(ctx context.Context, label string, firesAt time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO timers (label, fires_at) VALUES (?, ?)`, label, ts(firesAt))
	if err != nil {
		return 0, fmt.Errorf("store: create timer: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) queryTimers(ctx context.Context, where string, args ...any) ([]Timer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, label, fires_at FROM timers WHERE status = 'pending' `+where+` ORDER BY fires_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: timers: %w", err)
	}
	defer rows.Close()
	var out []Timer
	for rows.Next() {
		var t Timer
		var at string
		if err := rows.Scan(&t.ID, &t.Label, &at); err != nil {
			return nil, err
		}
		t.FiresAt = parseTime(at)
		out = append(out, t)
	}
	return out, rows.Err()
}

// PendingTimers returns timers that have not fired, soonest first.
func (s *Store) PendingTimers(ctx context.Context) ([]Timer, error) { return s.queryTimers(ctx, "") }

// DueTimers returns pending timers whose time has come.
func (s *Store) DueTimers(ctx context.Context, now time.Time) ([]Timer, error) {
	return s.queryTimers(ctx, `AND fires_at <= ?`, ts(now))
}

// MarkTimerFired closes a timer. It reports false if it was already closed.
func (s *Store) MarkTimerFired(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE timers SET status = 'fired' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CancelTimers cancels pending timers whose label contains match (all when match is empty).
func (s *Store) CancelTimers(ctx context.Context, match string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE timers SET status = 'cancelled' WHERE status = 'pending' AND lower(label) LIKE ? ESCAPE '\'`, likeContains(match))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CancelTimer cancels one timer by id.
func (s *Store) CancelTimer(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE timers SET status = 'cancelled' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Reminder is something to say at a given time.
type Reminder struct {
	ID     int64
	Text   string
	DueAt  time.Time
	Source string
}

// CreateReminder adds a pending reminder.
func (s *Store) CreateReminder(ctx context.Context, text string, dueAt time.Time, source string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO reminders (text, due_at, source) VALUES (?, ?, ?)`, text, ts(dueAt), source)
	if err != nil {
		return 0, fmt.Errorf("store: create reminder: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) queryReminders(ctx context.Context, where string, args ...any) ([]Reminder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, text, due_at, source FROM reminders WHERE status = 'pending' `+where+` ORDER BY due_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reminders: %w", err)
	}
	defer rows.Close()
	var out []Reminder
	for rows.Next() {
		var r Reminder
		var at string
		if err := rows.Scan(&r.ID, &r.Text, &at, &r.Source); err != nil {
			return nil, err
		}
		r.DueAt = parseTime(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingReminders returns reminders not yet spoken, soonest first.
func (s *Store) PendingReminders(ctx context.Context) ([]Reminder, error) {
	return s.queryReminders(ctx, "")
}

// DueReminders returns pending reminders whose time has come.
func (s *Store) DueReminders(ctx context.Context, now time.Time) ([]Reminder, error) {
	return s.queryReminders(ctx, `AND due_at <= ?`, ts(now))
}

// MarkReminderDone closes a reminder. It reports false if it was already closed.
func (s *Store) MarkReminderDone(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE reminders SET status = 'done' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CancelReminders cancels pending reminders whose text contains match.
func (s *Store) CancelReminders(ctx context.Context, match string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE reminders SET status = 'cancelled' WHERE status = 'pending' AND lower(text) LIKE ? ESCAPE '\'`, likeContains(match))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CancelReminder cancels one reminder by id.
func (s *Store) CancelReminder(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE reminders SET status = 'cancelled' WHERE id = ? AND status = 'pending'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneSkills deletes finished timers and reminders older than cutoff.
func (s *Store) PruneSkills(ctx context.Context, cutoff time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM timers WHERE status <> 'pending' AND fires_at < ?`, ts(cutoff)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM reminders WHERE status <> 'pending' AND due_at < ?`, ts(cutoff))
	return err
}

// likeContains builds a LIKE pattern matching text that contains s, escaping wildcards.
func likeContains(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
	return "%" + s + "%"
}
