// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/checks"
	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/driver"
	"github.com/redrillhq/redrill/internal/redact"
)

// fakeMounter serves a prepared tree as the "mount" and records lifecycle.
type fakeMounter struct {
	root      string
	mountErr  error
	mounted   string // snapshot ID passed to Mount
	unmounted bool
}

func (f *fakeMounter) Mount(_ context.Context, snapshotID, _ string) (driver.MountHandle, error) {
	if f.mountErr != nil {
		return nil, f.mountErr
	}
	f.mounted = snapshotID
	return fakeHandle{f}, nil
}

type fakeHandle struct{ m *fakeMounter }

func (h fakeHandle) Root() string   { return h.m.root }
func (h fakeHandle) Unmount() error { h.m.unmounted = true; return nil }

func mountStep(t *testing.T, snapshot string) StepSpec {
	t.Helper()
	return StepSpec{
		RunID: 1, Level: "l2",
		Source: config.Source{Name: "b", Type: "borg", Repo: "/r"},
		L2: &config.L2{
			Restore: config.Restore{Mode: "mount"},
			Checks:  []config.Check{{Kind: "path_exists", Path: "data/app.db"}},
		},
		Scratch:  config.Scratch{Dir: t.TempDir()},
		Now:      base,
		Snapshot: snapshot,
	}
}

func TestMountAndCheckPass(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "data", "app.db"), []byte("dbdbdb"), 0o600); err != nil {
		t.Fatal(err)
	}
	fm := &fakeMounter{root: tree}
	listCalled := false
	list := func(context.Context) ([]driver.Snapshot, error) {
		listCalled = true
		return []driver.Snapshot{{ID: "arch-9", Time: base.Add(-time.Hour)}}, nil
	}

	res, err := mountAndCheck(context.Background(), mountStep(t, ""), fm, list, "no archives", redact.New())
	if err != nil {
		t.Fatalf("mountAndCheck: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s (%s), want pass", res.Status, res.Summary)
	}
	if !listCalled || fm.mounted != "arch-9" || res.Snapshot != "arch-9" {
		t.Errorf("unpinned resolution: list=%v mounted=%q snapshot=%q", listCalled, fm.mounted, res.Snapshot)
	}
	if res.SnapshotTime.IsZero() {
		t.Error("snapshot time lost (the RPO input)")
	}
	if !fm.unmounted {
		t.Error("mount left mounted after checks")
	}
	if res.Bytes != 0 {
		t.Errorf("bytes = %d, want 0 (nothing is copied under a mount)", res.Bytes)
	}
	if res.Files == 0 {
		t.Error("files = 0, want the walked count (the tolerance baseline)")
	}
}

// A pinned mount never lists the repo — same frugality as pinned copy mode.
func TestMountAndCheckPinnedSkipsListing(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "data", "app.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fm := &fakeMounter{root: tree}
	list := func(context.Context) ([]driver.Snapshot, error) {
		return nil, errors.New("list must not be called under a pin")
	}
	res, err := mountAndCheck(context.Background(), mountStep(t, "pinned-arch"), fm, list, "no archives", redact.New())
	if err != nil {
		t.Fatalf("mountAndCheck: %v", err)
	}
	if fm.mounted != "pinned-arch" || res.Snapshot != "pinned-arch" {
		t.Errorf("pin not honored: mounted=%q snapshot=%q", fm.mounted, res.Snapshot)
	}
}

// No FUSE (or any mount failure) is an error with a diagnosis — never a
// silent fallback to copy, never a pass.
func TestMountAndCheckMountFailureIsError(t *testing.T) {
	t.Parallel()
	fm := &fakeMounter{mountErr: errors.New("fuse: device not found")}
	list := func(context.Context) ([]driver.Snapshot, error) {
		return []driver.Snapshot{{ID: "a"}}, nil
	}
	res, err := mountAndCheck(context.Background(), mountStep(t, ""), fm, list, "no archives", redact.New())
	if err != nil {
		t.Fatalf("mountAndCheck: %v", err)
	}
	if res.Status != checks.Error {
		t.Fatalf("status = %s, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "FUSE") {
		t.Errorf("summary = %q, want the FUSE diagnosis", res.Summary)
	}
}
