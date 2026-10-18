package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PutSession saves a sign-in under the hash of its token.
func (s *Store) PutSession(ctx context.Context, tokenHash string, userID int64, fingerprint string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, fingerprint, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, fingerprint, ts(expires))
	if err != nil {
		return fmt.Errorf("store: put session: %w", err)
	}
	return nil
}

// GetSession returns a sign-in, or ok=false when there is none (an expired one counts as none).
func (s *Store) GetSession(ctx context.Context, tokenHash string, now time.Time) (userID int64, fingerprint string, ok bool, err error) {
	var expires string
	err = s.db.QueryRowContext(ctx, `SELECT user_id, fingerprint, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&userID, &fingerprint, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("store: get session: %w", err)
	}
	if now.After(parseTime(expires)) {
		return 0, "", false, nil
	}
	return userID, fingerprint, true, nil
}

// DeleteSession ends one sign-in.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// PurgeSessions removes every sign-in that has expired.
func (s *Store) PurgeSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ts(now))
	if err != nil {
		return fmt.Errorf("store: purge sessions: %w", err)
	}
	return nil
}
