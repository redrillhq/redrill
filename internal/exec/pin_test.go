// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/checks"
	"github.com/redrillhq/redrill/internal/config"
)

// recordingBorg wraps fakeBorg, recording every argv the driver runs.
type recordingBorg struct {
	fakeBorg
	mu    sync.Mutex
	calls [][]string
}

func (r *recordingBorg) run(ctx context.Context, dir string, env []string, name string, args []string) ([]byte, []byte, int, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.mu.Unlock()
	return r.fakeBorg.run(ctx, dir, env, name, args)
}

// repoListings counts repo-level archive listings (borg list --json <repo>),
// the round-trip a pinned step must skip.
func (r *recordingBorg) repoListings() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == "list" && !slices.Contains(call, "--short") && !slices.Contains(call, "--json-lines") {
			n++
		}
	}
	return n
}

func borgL2Step(t *testing.T, snapshot string) StepSpec {
	t.Helper()
	files := 1
	return StepSpec{
		RunID: 1, Level: "l2",
		Source:   config.Source{Name: "b", Type: "borg", Repo: "/r"},
		L2:       &config.L2{Restore: config.Restore{Sample: &config.Sample{Files: files}}},
		Scratch:  config.Scratch{Dir: t.TempDir()},
		Now:      base,
		Snapshot: snapshot,
	}
}

// A pinned borg L2 audits exactly the pinned archive and never re-lists the
// repository (one snapshot per run; restores are heavy, IO is frugal).
func TestBorgL2PinSkipsListing(t *testing.T) {
	t.Parallel()
	f := &recordingBorg{fakeBorg: fakeBorg{listFilesJSON: borgFilesJSON(100)}}
	e := NewLocal("h")
	e.borgRunner = f.run

	res, err := e.RunStep(context.Background(), borgL2Step(t, "pinned-arch"))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Snapshot != "pinned-arch" {
		t.Errorf("res.Snapshot = %q, want the pin echoed back", res.Snapshot)
	}
	if n := f.repoListings(); n != 0 {
		t.Errorf("repo listed %d times under a pin, want 0", n)
	}
	var extracted bool
	for _, call := range f.calls {
		if call[0] == "extract" && slices.Contains(call, "/r::pinned-arch") {
			extracted = true
		}
	}
	if !extracted {
		t.Errorf("extract did not target the pinned archive: %v", f.calls)
	}
}

// Without a pin, L2 resolves newest and reports it back for the rest of the run.
func TestBorgL2UnpinnedResolvesNewest(t *testing.T) {
	t.Parallel()
	f := &recordingBorg{fakeBorg: fakeBorg{
		listJSON:      borgListJSON(base.Add(-2*time.Hour), base.Add(-time.Hour)),
		listFilesJSON: borgFilesJSON(100),
	}}
	e := NewLocal("h")
	e.borgRunner = f.run

	res, err := e.RunStep(context.Background(), borgL2Step(t, ""))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Snapshot != "arch-2" {
		t.Errorf("res.Snapshot = %q, want arch-2 (newest)", res.Snapshot)
	}
	if n := f.repoListings(); n != 1 {
		t.Errorf("repo listed %d times unpinned, want 1", n)
	}
}

// A pinned dumpdir L2 restores exactly the dump an earlier level audited,
// even when a newer dump landed mid-run — the TOCTOU the pin exists for.
func TestDumpdirL2PinBeatsNewerDump(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDump(t, dir, "old.sql.gz", base.Add(-2*time.Hour))
	writeDump(t, dir, "new.sql.gz", base.Add(-time.Minute)) // "landed mid-run"

	res, err := NewLocal("h").RunStep(context.Background(), pinnedDumpdirL2Step(t, dir, "old.sql.gz"))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Snapshot != "old.sql.gz" {
		t.Errorf("res.Snapshot = %q, want the pinned dump", res.Snapshot)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s (%s), want pass", res.Status, res.Summary)
	}
}

// A pinned dump overwritten in place (same name, new bytes, new mtime) is an
// error — dumpdir's name-based pin verifies the audited timestamp.
func TestDumpdirL2PinOverwrittenIsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	auditedAt := base.Add(-2 * time.Hour)
	writeDump(t, dir, "app.sql.gz", auditedAt)

	step := pinnedDumpdirL2Step(t, dir, "app.sql.gz")
	step.SnapshotTime = auditedAt // what L1 saw

	// The overwrite: same filename, fresh mtime.
	writeDump(t, dir, "app.sql.gz", base.Add(-time.Minute))

	res, err := NewLocal("h").RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Error {
		t.Fatalf("status = %s, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "changed since it was audited") {
		t.Errorf("summary = %q, want the changed-pin diagnosis", res.Summary)
	}

	// Unchanged mtime still passes the guard.
	fresh := pinnedDumpdirL2Step(t, dir, "app.sql.gz")
	fresh.SnapshotTime = base.Add(-time.Minute)
	res, err = NewLocal("h").RunStep(context.Background(), fresh)
	if err != nil || res.Status != checks.Pass {
		t.Fatalf("matching mtime: status=%s err=%v, want pass", res.Status, err)
	}
}

// A pinned dump that vanished is an error (the auditor cannot audit what the
// run started on), never a silent switch to a different dump.
func TestDumpdirL2PinMissingIsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDump(t, dir, "new.sql.gz", base.Add(-time.Minute))

	res, err := NewLocal("h").RunStep(context.Background(), pinnedDumpdirL2Step(t, dir, "ghost.sql.gz"))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Error {
		t.Fatalf("status = %s, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "pinned dump") {
		t.Errorf("summary = %q, want the pinned-dump diagnosis", res.Summary)
	}
}

// Dumpdir L1 reports the dump it audited so later levels pin to it.
func TestDumpdirL1ReportsPin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDump(t, dir, "old.sql.gz", base.Add(-2*time.Hour))
	writeDump(t, dir, "new.sql.gz", base.Add(-time.Hour))

	size := config.Size(1)
	step := StepSpec{
		RunID: 1, Level: "l1",
		Source: config.Source{Name: "d", Type: "dumpdir", Path: dir, Pattern: "*.sql.gz"},
		L1:     &config.L1{FileMinBytes: &size},
		Now:    base,
	}
	res, err := NewLocal("h").RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Snapshot != "new.sql.gz" {
		t.Errorf("res.Snapshot = %q, want the newest dump", res.Snapshot)
	}
}

// pinnedDumpdirL2Step is exec_test's dumpdirL2Step with a snapshot pin set.
func pinnedDumpdirL2Step(t *testing.T, dir, snapshot string) StepSpec {
	t.Helper()
	step := dumpdirL2Step(dir, []config.Check{{Kind: "min_total_bytes", MinTotalBytes: config.Size(1)}}, t.TempDir(), 0)
	step.Snapshot = snapshot
	return step
}

func writeDump(t *testing.T, dir, name string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("dump-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
