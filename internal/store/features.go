package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// FeatureRow is a persisted feature flag.
type FeatureRow struct {
	Name    string
	Enabled bool
}

// EnsureFeature creates the feature row (disabled) if it does not exist yet.
func (s *Store) EnsureFeature(ctx context.Context, name string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO features (name) VALUES (?)`, name); err != nil {
		return fmt.Errorf("store: ensure feature %q: %w", name, err)
	}
	return nil
}

// SetFeatureEnabled updates a feature flag. It returns ErrNotFound if the
// feature was never ensured.
func (s *Store) SetFeatureEnabled(ctx context.Context, name string, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE features SET enabled = ?, updated_at = `+nowSQL+` WHERE name = ?`, enabled, name)
	if err != nil {
		return fmt.Errorf("store: set feature %q: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFeatures returns all feature flags ordered by name.
func (s *Store) ListFeatures(ctx context.Context) ([]FeatureRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, enabled FROM features ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list features: %w", err)
	}
	defer rows.Close()
	var out []FeatureRow
	for rows.Next() {
		var f FeatureRow
		if err := rows.Scan(&f.Name, &f.Enabled); err != nil {
			return nil, fmt.Errorf("store: scan feature: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FeatureEnabled reports whether a feature is enabled. Unknown features are disabled.
func (s *Store) FeatureEnabled(ctx context.Context, name string) (bool, error) {
	var on bool
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM features WHERE name = ?`, name).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: feature enabled %q: %w", name, err)
	}
	return on, nil
}
