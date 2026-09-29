package store

import (
	"context"
	"fmt"
)

// DeviceName returns the device's display name.
func (s *Store) DeviceName(ctx context.Context) (string, error) {
	var n string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM device WHERE id = 1`).Scan(&n); err != nil {
		return "", fmt.Errorf("store: device name: %w", err)
	}
	return n, nil
}

// SetDeviceName changes the device's display name.
func (s *Store) SetDeviceName(ctx context.Context, name string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE device SET name = ? WHERE id = 1`, name); err != nil {
		return fmt.Errorf("store: set device name: %w", err)
	}
	return nil
}
