package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AdminUser is a person who can sign in to the web admin.
type AdminUser struct {
	ID           int64
	Username     string
	PasswordHash string
	IsOwner      bool
	MustChange   bool
	CreatedAt    time.Time
	LastLogin    *time.Time
}

const adminCols = `id, username, password_hash, is_owner, must_change, created_at, last_login`

func scanAdmin(sc interface{ Scan(...any) error }) (AdminUser, error) {
	var a AdminUser
	var created string
	var last sql.NullString
	if err := sc.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.IsOwner, &a.MustChange, &created, &last); err != nil {
		return AdminUser{}, err
	}
	a.CreatedAt = parseTime(created)
	if last.Valid {
		t := parseTime(last.String)
		a.LastLogin = &t
	}
	return a, nil
}

// CreateAdmin adds an admin account.
func (s *Store) CreateAdmin(ctx context.Context, username, passwordHash string, owner, mustChange bool) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO admin_users (username, password_hash, is_owner, must_change) VALUES (?, ?, ?, ?)`,
		username, passwordHash, owner, mustChange)
	if err != nil {
		return 0, fmt.Errorf("store: create admin: %w", err)
	}
	return res.LastInsertId()
}

// GetAdmin returns an admin by id, or ErrNotFound.
func (s *Store) GetAdmin(ctx context.Context, id int64) (AdminUser, error) {
	a, err := scanAdmin(s.db.QueryRowContext(ctx, `SELECT `+adminCols+` FROM admin_users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	}
	return a, err
}

// GetAdminByName returns an admin by username, or ErrNotFound.
func (s *Store) GetAdminByName(ctx context.Context, username string) (AdminUser, error) {
	a, err := scanAdmin(s.db.QueryRowContext(ctx, `SELECT `+adminCols+` FROM admin_users WHERE username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	}
	return a, err
}

// ListAdmins returns all admins, owner first.
func (s *Store) ListAdmins(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+adminCols+` FROM admin_users ORDER BY is_owner DESC, username`)
	if err != nil {
		return nil, fmt.Errorf("store: list admins: %w", err)
	}
	defer rows.Close()
	var out []AdminUser
	for rows.Next() {
		a, err := scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAdminPassword replaces an admin's password hash.
func (s *Store) SetAdminPassword(ctx context.Context, id int64, hash string, mustChange bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE admin_users SET password_hash = ?, must_change = ? WHERE id = ?`, hash, mustChange, id)
	if err != nil {
		return fmt.Errorf("store: set admin password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAdminLogin records a successful sign-in.
func (s *Store) TouchAdminLogin(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_users SET last_login = ? WHERE id = ?`, ts(now), id)
	return err
}

// DeleteAdmin removes an admin account.
func (s *Store) DeleteAdmin(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM admin_users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
