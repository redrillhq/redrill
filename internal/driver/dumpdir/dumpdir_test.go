// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package dumpdir

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/driver"
)

var base = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func writeDump(t *testing.T, dir, name, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestListSnapshotsNewestFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDump(t, dir, "app-1.sql.gz", "a", base.Add(-2*time.Hour))
	writeDump(t, dir, "app-2.sql.gz", "bb", base.Add(-1*time.Hour))
	writeDump(t, dir, "app-3.sql.gz", "ccc", base)
	writeDump(t, dir, "notes.txt", "ignore me", base) // non-matching
	if err := os.Mkdir(filepath.Join(dir, "app-sub.sql.gz"), 0o755); err != nil {
		t.Fatal(err) // matching directory must be skipped
	}

	snaps, err := New(dir, "app-*.sql.gz").ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 3 {
		t.Fatalf("got %d snapshots, want 3: %+v", len(snaps), snaps)
	}
	if snaps[0].ID != "app-3.sql.gz" || snaps[2].ID != "app-1.sql.gz" {
		t.Errorf("not newest-first: %s .. %s", snaps[0].ID, snaps[2].ID)
	}
	if snaps[0].Size != 3 || snaps[0].Time.Location() != time.UTC {
		t.Errorf("snapshot meta wrong: %+v", snaps[0])
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := New(dir, "*.gz")
	if err := d.Validate(context.Background()); err != nil {
		t.Errorf("Validate(readable dir) = %v, want nil", err)
	}
	if err := New(filepath.Join(dir, "nope"), "*.gz").Validate(context.Background()); err == nil {
		t.Error("Validate(missing dir) = nil, want error")
	}
	if err := New(dir, "[").Validate(context.Background()); err == nil {
		t.Error("Validate(bad pattern) = nil, want error")
	}
}

func TestListSnapshotsMissingDirErrors(t *testing.T) {
	t.Parallel()
	_, err := New(filepath.Join(t.TempDir(), "nope"), "*.gz").ListSnapshots(context.Background())
	if err == nil {
		t.Fatal("want error for missing/unreadable dir, not empty list")
	}
}

func TestRestoreCopiesReadOnly(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	writeDump(t, src, "a.sql.gz", "alpha", base)
	writeDump(t, src, "b.sql.gz", "beta", base)
	target := t.TempDir()

	d := New(src, "*.sql.gz")
	rep, err := d.Restore(context.Background(), driver.Selection{SnapshotIDs: []string{"a.sql.gz", "b.sql.gz"}}, target)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rep.Files != 2 || rep.Bytes != int64(len("alpha")+len("beta")) {
		t.Errorf("report = %+v, want 2 files / 9 bytes", rep)
	}
	got, err := os.ReadFile(filepath.Join(target, "a.sql.gz"))
	if err != nil || string(got) != "alpha" {
		t.Errorf("restored a.sql.gz = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(src, "a.sql.gz")); err != nil {
		t.Errorf("source file disturbed: %v", err)
	}
}

func TestCapabilitiesAndName(t *testing.T) {
	t.Parallel()
	d := New(t.TempDir(), "*.gz")
	if d.Name() != "dumpdir" {
		t.Errorf("Name = %q", d.Name())
	}
	caps := d.Capabilities()
	if caps.NativeCheck || !caps.ListSnapshots || !caps.PartialRestore {
		t.Errorf("caps = %+v", caps)
	}
}

func TestNativeCheckUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := New(t.TempDir(), "*.gz").NativeCheck(context.Background(), driver.NativeCheckOpts{}); err == nil {
		t.Error("NativeCheck on dumpdir should error (no native check)")
	}
}
