package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const lastProjectKey = "last_project"

// SetLastProject records the most recently opened project name.
func (s *Store) SetLastProject(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ui_state(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, lastProjectKey, name)
	if err != nil {
		return fmt.Errorf("set UI state %q: %w", lastProjectKey, err)
	}
	return nil
}

// LastProject returns the most recently opened project name and whether it is set.
func (s *Store) LastProject(ctx context.Context) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM ui_state WHERE key=?`, lastProjectKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read UI state %q: %w", lastProjectKey, err)
	}
	return value, true, nil
}
