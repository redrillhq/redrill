package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// epoch is the deterministic base time for tests; the store carries no clock, so
// every timestamp is supplied explicitly (TESTING.md determinism).
var epoch = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "drillbit.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestLoadMigrations(t *testing.T) {
	t.Parallel()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("migrations = %d, want 1", len(ms))
	}
	if ms[0].version != 1 || ms[0].name != "0001_init.sql" {
		t.Errorf("migration[0] = {%d, %q}, want {1, 0001_init.sql}", ms[0].version, ms[0].name)
	}
}

func TestOpenMigratesFromEmpty(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version = %d, want 1", version)
	}

	want := map[string]bool{
		"sources": true, "drills": true, "runs": true, "run_steps": true,
		"evidence": true, "artifacts": true, "drill_state": true,
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for tbl := range want {
		if !got[tbl] {
			t.Errorf("missing table %q", tbl)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "drillbit.db")

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// Write a row, then reopen: a second migrate pass must not error or wipe data.
	if err := s1.UpsertSource(ctx, Source{Name: "s", Type: "borg", CreatedAt: epoch}); err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	if _, err := s2.GetSource(ctx, "s"); err != nil {
		t.Errorf("data lost across reopen: %v", err)
	}
}

func TestOpenBadPath(t *testing.T) {
	t.Parallel()
	// A directory that does not exist cannot be created as a DB file.
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "nope", "drillbit.db"))
	if err == nil {
		t.Fatal("want error opening under a missing directory")
	}
}
