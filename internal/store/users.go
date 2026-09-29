package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// User is an authorized person known to the device.
type User struct {
	ID          int64
	DisplayName string
	Role        string
	Active      bool
}

// CreateUser inserts a user. The role must be one of the roles allowed by the schema.
func (s *Store) CreateUser(ctx context.Context, displayName, role string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (display_name, role) VALUES (?, ?)`, displayName, role)
	if err != nil {
		return 0, fmt.Errorf("store: create user: %w", err)
	}
	return res.LastInsertId()
}

// GetUser returns the user with the given id, or ErrNotFound.
func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id, display_name, role, active FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.DisplayName, &u.Role, &u.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: get user: %w", err)
	}
	return u, nil
}

// SetPermission records an explicit grant or denial for a user.
func (s *Store) SetPermission(ctx context.Context, userID int64, permission string, granted bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO permissions (user_id, permission, granted) VALUES (?, ?, ?)
		ON CONFLICT (user_id, permission) DO UPDATE SET granted = excluded.granted`, userID, permission, granted)
	if err != nil {
		return fmt.Errorf("store: set permission: %w", err)
	}
	return nil
}

// UserPermissions returns the explicit overrides for a user (permission -> granted).
func (s *Store) UserPermissions(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT permission, granted FROM permissions WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: user permissions: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		var g bool
		if err := rows.Scan(&p, &g); err != nil {
			return nil, fmt.Errorf("store: scan permission: %w", err)
		}
		out[p] = g
	}
	return out, rows.Err()
}
