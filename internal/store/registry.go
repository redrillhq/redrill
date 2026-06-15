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

// ErrNotFound is returned (wrapped) by Get* lookups when no row matches.
var ErrNotFound = errors.New("not found")

// UpsertSource preserves created_at on update.
func (s *Store) UpsertSource(ctx context.Context, src Source) error {
	if src.Name == "" {
		return fmt.Errorf("upsert source: name required")
	}
	if src.CreatedAt.IsZero() {
		return fmt.Errorf("upsert source %s: created_at required", src.Name)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sources (name, type, config_hash, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET type = excluded.type, config_hash = excluded.config_hash`,
		src.Name, src.Type, src.ConfigHash, unixNano(src.CreatedAt))
	if err != nil {
		return fmt.Errorf("upsert source %s: %w", src.Name, err)
	}
	return nil
}

// GetSource returns wrapped ErrNotFound when absent.
func (s *Store) GetSource(ctx context.Context, name string) (Source, error) {
	var (
		src     Source
		created int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT name, type, config_hash, created_at FROM sources WHERE name = ?`, name).
		Scan(&src.Name, &src.Type, &src.ConfigHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, fmt.Errorf("get source %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Source{}, fmt.Errorf("get source %s: %w", name, err)
	}
	src.CreatedAt = timeFromUnixNano(created)
	return src, nil
}

// ListSources is ordered by name.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, type, config_hash, created_at FROM sources ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Source
	for rows.Next() {
		var (
			src     Source
			created int64
		)
		if err := rows.Scan(&src.Name, &src.Type, &src.ConfigHash, &created); err != nil {
			return nil, fmt.Errorf("list sources: %w", err)
		}
		src.CreatedAt = timeFromUnixNano(created)
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	return out, nil
}

func (s *Store) UpsertDrill(ctx context.Context, d Drill) error {
	if d.Name == "" {
		return fmt.Errorf("upsert drill: name required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drills (name, source, config_hash, max_proof_age, levels_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			source = excluded.source,
			config_hash = excluded.config_hash,
			max_proof_age = excluded.max_proof_age,
			levels_json = excluded.levels_json`,
		d.Name, d.Source, d.ConfigHash, int64(d.MaxProofAge), d.LevelsJSON)
	if err != nil {
		return fmt.Errorf("upsert drill %s: %w", d.Name, err)
	}
	return nil
}

// GetDrill returns wrapped ErrNotFound when absent.
func (s *Store) GetDrill(ctx context.Context, name string) (Drill, error) {
	var (
		d           Drill
		maxProofAge int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT name, source, config_hash, max_proof_age, levels_json FROM drills WHERE name = ?`, name).
		Scan(&d.Name, &d.Source, &d.ConfigHash, &maxProofAge, &d.LevelsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Drill{}, fmt.Errorf("get drill %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Drill{}, fmt.Errorf("get drill %s: %w", name, err)
	}
	d.MaxProofAge = time.Duration(maxProofAge)
	return d, nil
}

// ListDrills is ordered by name.
func (s *Store) ListDrills(ctx context.Context) ([]Drill, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, source, config_hash, max_proof_age, levels_json FROM drills ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list drills: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Drill
	for rows.Next() {
		var (
			d           Drill
			maxProofAge int64
		)
		if err := rows.Scan(&d.Name, &d.Source, &d.ConfigHash, &maxProofAge, &d.LevelsJSON); err != nil {
			return nil, fmt.Errorf("list drills: %w", err)
		}
		d.MaxProofAge = time.Duration(maxProofAge)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list drills: %w", err)
	}
	return out, nil
}
