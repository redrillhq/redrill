// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the cgo-free "sqlite" driver
)

// Store is clock-free: every business timestamp is caller-supplied.
type Store struct {
	db *sql.DB
}

// Open creates the database if absent and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// One connection serializes all access against the single file, so concurrent
	// writers (scheduler runs + the staleness sweeper) never hit SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

// dsn carries the pragmas every pooled connection needs. foreign_keys is
// per-connection in SQLite, so it must live in the DSN.
func dsn(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

func unixNano(t time.Time) int64 { return t.UTC().UnixNano() }

func timeFromUnixNano(n int64) time.Time { return time.Unix(0, n).UTC() }

// nullTime maps the zero time to SQL NULL (e.g. an unfinished run).
func nullTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().UnixNano(), Valid: true}
}

// timeFromNull maps SQL NULL to the zero time.
func timeFromNull(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(0, n.Int64).UTC()
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
