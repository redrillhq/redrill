// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package restic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/driver"
)

// Real-engine tests against restic. Test setup writes the fixture repo (restic
// init/backup); the driver under test only ever reads. Skipped without restic.

func requireRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed; run via make test-integration in an environment with restic")
	}
}

// buildRepo creates a repo with one snapshot of a config/ + data/ tree; age>0
// backdates it via --time. Returns the repo path and env.
func buildRepo(t *testing.T, age time.Duration) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	mkdir(t, filepath.Join(src, "config"))
	write(t, filepath.Join(src, "config", "config.php"), "<?php // fixture")
	mkdir(t, filepath.Join(src, "data", "docs"))
	write(t, filepath.Join(src, "data", "docs", "a.txt"), strings.Repeat("payload\n", 200))

	env := append(os.Environ(), "RESTIC_PASSWORD=testpass", "RESTIC_REPOSITORY="+repo)
	runRestic(t, dir, env, "init")
	args := []string{"backup"}
	if age > 0 {
		args = append(args, "--time", time.Now().UTC().Add(-age).Format("2006-01-02 15:04:05"))
	}
	args = append(args, src)
	runRestic(t, dir, env, args...)
	return repo, env
}

func driverFor(repo string) *Driver { return New(repo, WithPassword("testpass")) }

func TestIntegrationDriver(t *testing.T) {
	requireRestic(t)
	ctx := context.Background()
	repo, _ := buildRepo(t, 0)
	d := driverFor(repo)

	if err := d.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	snaps, err := d.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snaps))
	}
	if time.Since(snaps[0].Time) > time.Hour {
		t.Errorf("snapshot looks too old: %v", snaps[0].Time)
	}

	rep, err := d.NativeCheck(ctx, driver.NativeCheckOpts{})
	if err != nil {
		t.Fatalf("NativeCheck: %v", err)
	}
	if !rep.OK {
		t.Errorf("healthy repo: NativeCheck OK=false (%s)", rep.Summary)
	}

	if size, err := d.SnapshotSize(ctx, snaps[0].ID); err != nil || size <= 0 {
		t.Errorf("SnapshotSize = %d, %v; want >0", size, err)
	}

	// ListFiles paths are relative (the /src root is stripped).
	files, err := d.ListFiles(ctx, snaps[0].ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if !hasFile(files, "config/config.php") {
		t.Errorf("config/config.php not in (relative) listing: %+v", files)
	}

	// Restore strips the root: config/config.php lands directly under target.
	target := t.TempDir()
	rr, err := d.Restore(ctx, driver.Selection{SnapshotIDs: []string{snaps[0].ID}, Paths: []string{"config/config.php"}}, target)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rr.Files != 1 {
		t.Errorf("restored %d files, want 1", rr.Files)
	}
	if _, err := os.Stat(filepath.Join(target, "config", "config.php")); err != nil {
		t.Errorf("restored file missing (root not stripped): %v", err)
	}
}

// Sabotage: a deleted pack — restic check must flag it.
func TestIntegrationMissingPack(t *testing.T) {
	requireRestic(t)
	ctx := context.Background()
	repo, _ := buildRepo(t, 0)

	if rep, err := driverFor(repo).NativeCheck(ctx, driver.NativeCheckOpts{}); err != nil || !rep.OK {
		t.Fatalf("pre-corruption check: OK=%v err=%v", rep.OK, err)
	}

	if err := os.Remove(largestFile(t, filepath.Join(repo, "data"))); err != nil {
		t.Fatal(err)
	}

	rep, err := driverFor(repo).NativeCheck(ctx, driver.NativeCheckOpts{})
	if err != nil {
		t.Fatalf("NativeCheck after corruption returned a Go error, want a failing report: %v", err)
	}
	if rep.OK {
		t.Error("missing-pack NOT caught: restic check reported OK")
	}
}

// Sabotage: stale-source. The newest snapshot is 30 days old.
func TestIntegrationStaleSource(t *testing.T) {
	requireRestic(t)
	ctx := context.Background()
	repo, _ := buildRepo(t, 30*24*time.Hour)

	snaps, err := driverFor(repo).ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) == 0 {
		t.Fatal("no snapshots")
	}
	if age := time.Since(snaps[0].Time); age < 36*time.Hour {
		t.Errorf("stale-source NOT caught: newest snapshot age %v, want old enough to fail snapshot_max_age", age)
	}
}

func hasFile(files []driver.FileEntry, rel string) bool {
	for _, f := range files {
		if f.IsFile && f.Path == rel {
			return true
		}
	}
	return false
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRestic(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "restic", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restic %v: %v\n%s", args, err, out)
	}
}

func largestFile(t *testing.T, root string) string {
	t.Helper()
	var biggest string
	var maxSize int64 = -1
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Size() > maxSize {
			maxSize, biggest = info.Size(), p
		}
		return nil
	})
	if err != nil || biggest == "" {
		t.Fatalf("find pack under %s: %v", root, err)
	}
	return biggest
}
