package store

import (
	"context"
	"fmt"
)

// SchemaSignature lists every table, index and trigger with its SQL, sorted. A database
// migrated by the same migration files has the same signature, so a restored copy can be
// compared with a freshly migrated one to see that its real schema is the expected one.
func (s *Store) SchemaSignature(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT type || '|' || name || '|' || tbl_name || '|' || IFNULL(sql, '')
		FROM sqlite_master WHERE name != 'sqlite_sequence' AND name NOT LIKE 'sqlite_stat%' ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("store: schema signature: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("store: schema signature: %w", err)
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

// ForeignKeyCheck reports rows that point at rows that do not exist.
func (s *Store) ForeignKeyCheck(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("store: foreign key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("store: database has rows that break a foreign key")
	}
	return rows.Err()
}
