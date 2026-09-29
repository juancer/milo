package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PutSecret stores an already-encrypted value. Encryption is done by
// internal/secrets; this package never sees plaintext.
func (s *Store) PutSecret(ctx context.Context, name string, ciphertext []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO secrets (name, ciphertext) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET ciphertext = excluded.ciphertext, updated_at = `+nowSQL, name, ciphertext)
	if err != nil {
		return fmt.Errorf("store: put secret %q: %w", name, err)
	}
	return nil
}

// GetSecret returns the stored ciphertext, or ErrNotFound.
func (s *Store) GetSecret(ctx context.Context, name string) ([]byte, error) {
	var ct []byte
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext FROM secrets WHERE name = ?`, name).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get secret %q: %w", name, err)
	}
	return ct, nil
}
