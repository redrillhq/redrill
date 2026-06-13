package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the cgo-free "sqlite" driver
)

// Store is the embedded SQLite state (DESIGN §9.3). Open applies migrations and
// returns a ready Store; the zero value is not usable. Timestamps are persisted
// as UTC unix nanoseconds. Store carries no clock: every business timestamp is
// supplied by the caller (StartedAt, FinishedAt, proof time, retention now).
type Store struct {
	db *sql.DB
}

// Open opens (creating if absent) the SQLite database file at path and applies
// all pending migrations. The caller must Close it.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
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

// Close releases the database handle.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

// dsn builds a modernc DSN carrying the pragmas every pooled connection needs:
// foreign keys on (cascade deletes back retention), WAL, and a busy timeout.
// foreign_keys is per-connection in SQLite, so it must live in the DSN.
func dsn(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

// unixNano renders a time for a NOT NULL timestamp column. Callers guard against
// the zero time before reaching here.
func unixNano(t time.Time) int64 { return t.UTC().UnixNano() }

// timeFromUnixNano reads a NOT NULL timestamp column.
func timeFromUnixNano(n int64) time.Time { return time.Unix(0, n).UTC() }

// nullTime maps a time to a nullable timestamp column: the zero time becomes
// SQL NULL (e.g. an unfinished run).
func nullTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().UnixNano(), Valid: true}
}

// timeFromNull reads a nullable timestamp column; NULL becomes the zero time.
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
