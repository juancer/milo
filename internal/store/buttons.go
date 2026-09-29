package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ButtonAction maps a press pattern to an action name and its JSON parameters.
type ButtonAction struct {
	Action     string
	Parameters string // JSON object
}

// Button is a physical input and its event -> action mapping.
type Button struct {
	ID        int64
	Name      string
	Color     string
	GPIO      int
	ActiveLow bool
	Enabled   bool
	Actions   map[string]ButtonAction // keyed by event type
}

// ListButtons returns all buttons ordered by id, with their actions.
func (s *Store) ListButtons(ctx context.Context) ([]Button, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, color, gpio, active_low, enabled FROM button_configs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list buttons: %w", err)
	}
	var out []Button
	for rows.Next() {
		b := Button{Actions: map[string]ButtonAction{}}
		if err := rows.Scan(&b.ID, &b.Name, &b.Color, &b.GPIO, &b.ActiveLow, &b.Enabled); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan button: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	arows, err := s.db.QueryContext(ctx, `SELECT button_id, event_type, action, parameters FROM button_actions`)
	if err != nil {
		return nil, fmt.Errorf("store: list button actions: %w", err)
	}
	defer arows.Close()
	byID := map[int64]*Button{}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	for arows.Next() {
		var id int64
		var event string
		var a ButtonAction
		if err := arows.Scan(&id, &event, &a.Action, &a.Parameters); err != nil {
			return nil, fmt.Errorf("store: scan button action: %w", err)
		}
		if b := byID[id]; b != nil {
			b.Actions[event] = a
		}
	}
	return out, arows.Err()
}

// GetButton returns one button or ErrNotFound.
func (s *Store) GetButton(ctx context.Context, id int64) (Button, error) {
	all, err := s.ListButtons(ctx)
	if err != nil {
		return Button{}, err
	}
	for _, b := range all {
		if b.ID == id {
			return b, nil
		}
	}
	return Button{}, ErrNotFound
}

// CreateButton inserts a button and its actions and returns the new id.
func (s *Store) CreateButton(ctx context.Context, b Button) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx,
		`INSERT INTO button_configs (name, color, gpio, active_low, enabled) VALUES (?, ?, ?, ?, ?)`,
		b.Name, b.Color, b.GPIO, b.ActiveLow, b.Enabled)
	if err != nil {
		return 0, fmt.Errorf("store: create button: %w", err)
	}
	id, _ := res.LastInsertId()
	if err := replaceButtonActions(ctx, tx, id, b.Actions); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateButton replaces a button's settings and actions atomically.
func (s *Store) UpdateButton(ctx context.Context, b Button) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx,
		`UPDATE button_configs SET name = ?, color = ?, gpio = ?, active_low = ?, enabled = ? WHERE id = ?`,
		b.Name, b.Color, b.GPIO, b.ActiveLow, b.Enabled, b.ID)
	if err != nil {
		return fmt.Errorf("store: update button: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := replaceButtonActions(ctx, tx, b.ID, b.Actions); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteButton removes a button (its actions cascade).
func (s *Store) DeleteButton(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM button_configs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete button: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func replaceButtonActions(ctx context.Context, tx *sql.Tx, id int64, actions map[string]ButtonAction) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM button_actions WHERE button_id = ?`, id); err != nil {
		return fmt.Errorf("store: clear button actions: %w", err)
	}
	for event, a := range actions {
		params := a.Parameters
		if params == "" {
			params = "{}"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO button_actions (button_id, event_type, action, parameters) VALUES (?, ?, ?, ?)`,
			id, event, a.Action, params); err != nil {
			return fmt.Errorf("store: set button action %q: %w", event, err)
		}
	}
	return nil
}

// IsNotFound is a convenience for callers outside this package.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
