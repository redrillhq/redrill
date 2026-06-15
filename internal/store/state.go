// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RecordProof advances last_proven_at for one (drill, level).
func (s *Store) RecordProof(ctx context.Context, drill, level string, at time.Time) error {
	if drill == "" || level == "" {
		return fmt.Errorf("record proof: drill and level required")
	}
	if at.IsZero() {
		return fmt.Errorf("record proof for %s/%s: timestamp required", drill, level)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drill_state (drill, level, last_proven_at)
		VALUES (?, ?, ?)
		ON CONFLICT(drill, level) DO UPDATE SET last_proven_at = excluded.last_proven_at`,
		drill, level, unixNano(at))
	if err != nil {
		return fmt.Errorf("record proof for %s/%s: %w", drill, level, err)
	}
	return nil
}

// GetProof returns ok=false (not an error) when the level was never proven.
func (s *Store) GetProof(ctx context.Context, drill, level string) (time.Time, bool, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_proven_at FROM drill_state WHERE drill = ? AND level = ?`, drill, level).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("get proof %s/%s: %w", drill, level, err)
	}
	return timeFromUnixNano(n), true, nil
}

// ListProofs is ordered by level.
func (s *Store) ListProofs(ctx context.Context, drill string) ([]DrillState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT drill, level, last_proven_at FROM drill_state WHERE drill = ? ORDER BY level`, drill)
	if err != nil {
		return nil, fmt.Errorf("list proofs for %s: %w", drill, err)
	}
	defer func() { _ = rows.Close() }()

	var out []DrillState
	for rows.Next() {
		var (
			ds     DrillState
			proven int64
		)
		if err := rows.Scan(&ds.Drill, &ds.Level, &proven); err != nil {
			return nil, fmt.Errorf("list proofs for %s: %w", drill, err)
		}
		ds.LastProvenAt = timeFromUnixNano(proven)
		out = append(out, ds)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list proofs for %s: %w", drill, err)
	}
	return out, nil
}
