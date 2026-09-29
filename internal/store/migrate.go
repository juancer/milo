package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

var migrationFiles fs.FS = mustSub(embeddedMigrations, "migrations")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version <= 0 {
			return nil, fmt.Errorf("store: migration %q must be named NNNN_description.sql", name)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", name, err)
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("store: duplicate migration version %d", out[i].version)
		}
	}
	return out, nil
}

// migrate applies pending migrations in order, each inside its own transaction.
// It is idempotent, and refuses to run against a database that is newer than
// this binary so a downgrade can never damage user data.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	migrations, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (`+nowSQL+`)
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read applied migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("store: scan applied migration: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: read applied migrations: %w", err)
	}
	rows.Close()

	known := map[int]bool{}
	for _, m := range migrations {
		known[m.version] = true
	}
	for v := range applied {
		if !known[v] {
			return fmt.Errorf("store: database has migration %d that this version of nexo does not know; refusing to run (was nexo downgraded?)", v)
		}
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %s: %w", m.name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: apply migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.version, m.name); err != nil {
		return fmt.Errorf("store: record migration %s: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %s: %w", m.name, err)
	}
	return nil
}
