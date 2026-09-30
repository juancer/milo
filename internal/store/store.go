// Package store is the only package that talks to SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: keeps the binary CGO-free
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// nowSQL is the SQL expression used for all timestamp defaults (UTC, RFC 3339).
const nowSQL = "strftime('%Y-%m-%dT%H:%M:%fZ','now')"

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies any pending
// migrations. The database runs in WAL mode with synchronous=NORMAL.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve path: %w", err)
	}
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(1)",
		"busy_timeout(5000)",
	} {
		q.Add("_pragma", p)
	}
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// A single connection serialises writes and avoids SQLITE_BUSY upgrades.
	// The workload (one small device) does not need more.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(ctx, db, migrationFiles); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	return int(v.Int64), nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Snapshot writes a consistent copy of the database to dest (which must not
// exist), safe to run while Milo is working. Uses SQLite's VACUUM INTO.
func (s *Store) Snapshot(ctx context.Context, dest string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("store: snapshot: %w", err)
	}
	return nil
}

// IntegrityCheck runs SQLite's integrity check and reports any damage.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var res string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return fmt.Errorf("store: integrity check: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("store: database is damaged: %s", res)
	}
	return nil
}

// ForceMigrationRow records a migration version as applied without running it.
// It exists so tests can imitate a database written by a newer Milo.
func (s *Store) ForceMigrationRow(ctx context.Context, version int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (?, 'forced')`, version)
	return err
}
