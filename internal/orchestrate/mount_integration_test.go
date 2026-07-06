// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package orchestrate

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	exe "github.com/redrillhq/redrill/internal/exec"
	"github.com/redrillhq/redrill/internal/fixtures"
	"github.com/redrillhq/redrill/internal/store"
)

// requireFUSE skips unless the host can serve FUSE mounts (Linux /dev/fuse
// plus an unmount tool) — the mount-mode integration gate.
func requireFUSE(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse; skipping mount-mode integration")
	}
	for _, tool := range []string{"fusermount3", "fusermount", "umount"} {
		if _, err := exec.LookPath(tool); err == nil {
			return
		}
	}
	t.Skip("no unmount tool; skipping mount-mode integration")
}

func mountL2(checks ...config.Check) config.Levels {
	return config.Levels{L2: &config.L2{
		Restore: config.Restore{Mode: "mount"},
		Checks:  checks,
	}}
}

// A borg mount-mode L2 drill proves restorability without copying: the
// archive tree is checked through the live FUSE mount.
func TestBorgMountModeL2(t *testing.T) {
	fixtures.RequireBorg(t)
	requireFUSE(t)
	repo, passFile := fixtures.Borg(t)
	st := newStore(t)

	drill := config.Drill{Name: "files", Source: "vault", Levels: mountL2(
		config.Check{Kind: "path_exists", Path: "data/"},
		config.Check{Kind: "min_total_bytes", MinTotalBytes: config.Size(1)},
	)}
	src := config.Source{Name: "vault", Type: "borg", Repo: repo, PassphraseFile: passFile}

	res := runDrill(t, st, drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if res.Status != store.ResultPass {
		t.Fatalf("mount-mode drill = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
	if res.Levels[0].Status != "pass" {
		t.Fatalf("l2 = %s, want pass", res.Levels[0].Status)
	}

	// The fail direction through the mount: a path the archive never held.
	bad := config.Drill{Name: "files", Source: "vault", Levels: mountL2(
		config.Check{Kind: "path_exists", Path: "data-that-was-never-backed-up/"},
	)}
	res = runDrill(t, st, bad, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if res.Status != store.ResultFail {
		t.Fatalf("missing-path mount drill = %s, want fail; levels = %+v", res.Status, res.Levels)
	}
}

// --keep leaves the L3 sandbox running after the run; the janitor reaping
// exactly what was left proves both the keep and the ephemerality story.
func TestKeepSandboxSurvivesRun(t *testing.T) {
	rt := requireDocker(t)
	dir := fixtures.Dump(t, fixtures.DumpBody("CREATE TABLE users(id int);\nINSERT INTO users VALUES (1);\n"))
	st := newStore(t)
	src := config.Source{Name: "dumps", Type: "dumpdir", Path: dir, Pattern: "*.sql.gz", Pick: "newest"}
	drill := config.Drill{Name: "app-db", Source: "dumps", Levels: config.Levels{
		L3: &config.L3{Sandbox: config.Sandbox{Image: pgImage(), Memory: config.Size(1 << 30)},
			Checks: []config.Check{{Kind: "sql_no_error", SQLNoError: "select 1"}}},
	}}
	o := New(st, exe.NewLocal("test").WithSandbox(rt), func() time.Time { return time.Now().UTC() })
	res, err := o.Run(context.Background(), drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}, Keep: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != store.ResultPass || res.KeptSandbox == "" {
		t.Fatalf("status=%s kept=%q, want pass with a kept sandbox", res.Status, res.KeptSandbox)
	}
	// The kept container is alive until the janitor reaps it — which is also
	// this test's cleanup.
	n, err := rt.Janitor(context.Background())
	if err != nil {
		t.Fatalf("janitor: %v", err)
	}
	if n < 1 {
		t.Fatalf("janitor reaped %d containers, want >=1 (the kept sandbox)", n)
	}
}

// The restic mirror, including the ids/<short>/<root> nesting Root resolves.
func TestResticMountModeL2(t *testing.T) {
	fixtures.RequireRestic(t)
	requireFUSE(t)
	repo, passFile := fixtures.Restic(t)
	st := newStore(t)

	drill := config.Drill{Name: "files", Source: "vault", Levels: mountL2(
		config.Check{Kind: "path_exists", Path: "data/"},
		config.Check{Kind: "newest_file_max_age", NewestFileMaxAge: config.Duration(24 * 365 * time.Hour)},
	)}
	src := config.Source{Name: "vault", Type: "restic", Repo: repo, PasswordFile: passFile}

	res := runDrill(t, st, drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if res.Status != store.ResultPass {
		t.Fatalf("restic mount-mode drill = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
}
