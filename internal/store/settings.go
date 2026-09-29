package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetSetting returns the value for key, or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: get setting %q: %w", key, err)
	}
	return v, nil
}

// SetSetting creates or replaces the value for key.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO system_settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = `+nowSQL, key, value)
	if err != nil {
		return fmt.Errorf("store: set setting %q: %w", key, err)
	}
	return nil
}
