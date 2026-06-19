// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package borg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/driver"
)

// Real-engine tests against borg 1.x. Test setup writes the fixture repo (borg
// init/create); the driver under test only ever reads. Skipped without borg.

func requireBorg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("borg"); err != nil {
		t.Skip("borg not installed; run via make test-integration in an environment with borg")
	}
}

// buildRepo creates an encrypted repo whose archives have the given ages (0 =
// now; >0 backdates via --timestamp). Returns the repo path and env.
func buildRepo(t *testing.T, ages map[string]time.Duration) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(src, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "config", "config.php"), "<?php // fixture")
	write(t, filepath.Join(src, "data.txt"), strings.Repeat("payload\n", 200))

	env := append(os.Environ(), "BORG_PASSPHRASE=testpass")
	runBorg(t, dir, env, "init", "--encryption=repokey", repo)

	names := make([]string, 0, len(ages))
	for n := range ages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		args := []string{"create"}
		if ages[name] > 0 {
			args = append(args, "--timestamp", time.Now().UTC().Add(-ages[name]).Format("2006-01-02T15:04:05"))
		}
		args = append(args, repo+"::"+name, ".")
		runBorg(t, src, env, args...)
	}
	return repo, env
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runBorg(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "borg", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("borg %v: %v\n%s", args, err, out)
	}
}

func driverFor(repo string, env []string) *Driver {
	pass := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "BORG_PASSPHRASE="); ok {
			pass = v
		}
	}
	return New(repo, WithPassphrase(pass))
}

func TestIntegrationDriver(t *testing.T) {
	requireBorg(t)
	ctx := context.Background()
	repo, env := buildRepo(t, map[string]time.Duration{"older": time.Hour, "newer": 0})
	d := driverFor(repo, env)

	if err := d.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	snaps, err := d.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d archives, want 2", len(snaps))
	}
	if snaps[0].ID != "newer" {
		t.Errorf("newest = %q, want newer", snaps[0].ID)
	}
	if time.Since(snaps[0].Time) > time.Hour {
		t.Errorf("newest archive looks too old: %v", snaps[0].Time)
	}

	rep, err := d.NativeCheck(ctx, driver.NativeCheckOpts{})
	if err != nil {
		t.Fatalf("NativeCheck: %v", err)
	}
	if !rep.OK {
		t.Errorf("healthy repo: NativeCheck OK=false (%s)", rep.Summary)
	}

	size, err := d.ArchiveSize(ctx, "newer")
	if err != nil || size <= 0 {
		t.Errorf("ArchiveSize = %d, %v; want >0", size, err)
	}

	target := t.TempDir()
	rr, err := d.Restore(ctx, driver.Selection{SnapshotIDs: []string{"newer"}, Paths: []string{"config/config.php"}}, target)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rr.Files != 1 {
		t.Errorf("restored %d files, want 1", rr.Files)
	}
	if _, err := os.Stat(filepath.Join(target, "config", "config.php")); err != nil {
		t.Errorf("restored file missing: %v", err)
	}
}

// Sabotage: truncated-segment — the one corruption engines catch themselves;
// borg check must flag it.
func TestIntegrationTruncatedSegment(t *testing.T) {
	requireBorg(t)
	ctx := context.Background()
	repo, env := buildRepo(t, map[string]time.Duration{"a": 0})

	if rep, err := driverFor(repo, env).NativeCheck(ctx, driver.NativeCheckOpts{}); err != nil || !rep.OK {
		t.Fatalf("pre-corruption check: OK=%v err=%v", rep.OK, err)
	}

	seg := largestFile(t, filepath.Join(repo, "data"))
	info, _ := os.Stat(seg)
	if err := os.Truncate(seg, info.Size()-64); err != nil {
		t.Fatal(err)
	}

	rep, err := driverFor(repo, env).NativeCheck(ctx, driver.NativeCheckOpts{})
	if err != nil {
		t.Fatalf("NativeCheck after corruption returned a Go error, want a failing report: %v", err)
	}
	if rep.OK {
		t.Error("truncated-segment NOT caught: borg check reported OK")
	}
}

// Sabotage: stale-source. The newest archive is 30 days old; its age must exceed
// a typical window, which is what snapshot_max_age flags as fail.
func TestIntegrationStaleSource(t *testing.T) {
	requireBorg(t)
	ctx := context.Background()
	repo, env := buildRepo(t, map[string]time.Duration{"stale": 30 * 24 * time.Hour})

	snaps, err := driverFor(repo, env).ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) == 0 {
		t.Fatal("no archives")
	}
	if age := time.Since(snaps[0].Time); age < 36*time.Hour {
		t.Errorf("stale-source NOT caught: newest archive age %v, want it old enough to fail snapshot_max_age", age)
	}
}

func largestFile(t *testing.T, root string) string {
	t.Helper()
	var biggest string
	var max int64 = -1
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Size() > max {
			max, biggest = info.Size(), p
		}
		return nil
	})
	if err != nil || biggest == "" {
		t.Fatalf("find segment under %s: %v", root, err)
	}
	return biggest
}
