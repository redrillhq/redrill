package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func restoreDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPathChecks(t *testing.T) {
	t.Parallel()
	dir := restoreDir(t, map[string]string{"config/config.php": "x", "CANARY": "marker"})
	env := CheckEnv{RestoreDir: dir, Now: now}
	ctx := context.Background()

	if ev, _ := (PathExists{Path: "config/config.php"}).Run(ctx, env); ev.Status != Pass {
		t.Errorf("path_exists present: %s", ev.Status)
	}
	if ev, _ := (PathExists{Path: "data/missing"}).Run(ctx, env); ev.Status != Fail {
		t.Errorf("path_exists missing: %s, want fail", ev.Status)
	}
	if ev, _ := (PathAbsent{Path: "data/missing"}).Run(ctx, env); ev.Status != Pass {
		t.Errorf("path_absent missing: %s, want pass", ev.Status)
	}
	if ev, _ := (PathAbsent{Path: "config/config.php"}).Run(ctx, env); ev.Status != Fail {
		t.Errorf("path_absent present: %s, want fail", ev.Status)
	}
	if ev, _ := (CanaryFile{Path: "CANARY"}).Run(ctx, env); ev.Status != Pass || !ev.Weak {
		t.Errorf("canary present: %s weak=%v, want pass/weak", ev.Status, ev.Weak)
	}
	if ev, _ := (CanaryFile{Path: "nope"}).Run(ctx, env); ev.Status != Fail || !ev.Weak {
		t.Errorf("canary missing: %s weak=%v, want fail/weak", ev.Status, ev.Weak)
	}
}

func TestNewestFileMaxAgeL2(t *testing.T) {
	t.Parallel()
	env := CheckEnv{Now: now}
	ctx := context.Background()
	if ev, _ := (NewestFileMaxAge{Newest: now.Add(-1 * time.Hour), Max: 8 * 24 * time.Hour}).Run(ctx, env); ev.Status != Pass {
		t.Errorf("fresh: %s", ev.Status)
	}
	if ev, _ := (NewestFileMaxAge{Newest: now.Add(-30 * 24 * time.Hour), Max: 8 * 24 * time.Hour}).Run(ctx, env); ev.Status != Fail {
		t.Errorf("stale: %s, want fail", ev.Status)
	}
	if ev, _ := (NewestFileMaxAge{Max: time.Hour}).Run(ctx, env); ev.Status != Error {
		t.Errorf("no files: %s, want error", ev.Status)
	}
}

func TestMinTotalBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if ev, _ := (MinTotalBytes{Total: 2048, Min: 1024}).Run(ctx, CheckEnv{}); ev.Status != Pass {
		t.Errorf("above min: %s", ev.Status)
	}
	if ev, _ := (MinTotalBytes{Total: 512, Min: 1024}).Run(ctx, CheckEnv{}); ev.Status != Fail {
		t.Errorf("below min: %s, want fail", ev.Status)
	}
}

func TestFileCountTolerance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if ev, _ := (FileCountTolerance{Count: 100, Prev: 0, Pct: 15}).Run(ctx, CheckEnv{}); ev.Status != Pass {
		t.Errorf("no baseline: %s, want pass", ev.Status)
	}
	if ev, _ := (FileCountTolerance{Count: 105, Prev: 100, Pct: 15}).Run(ctx, CheckEnv{}); ev.Status != Pass {
		t.Errorf("within tolerance: %s, want pass", ev.Status)
	}
	if ev, _ := (FileCountTolerance{Count: 50, Prev: 100, Pct: 15}).Run(ctx, CheckEnv{}); ev.Status != Fail {
		t.Errorf("50pct drop: %s, want fail", ev.Status)
	}
}

func TestHashMatch(t *testing.T) {
	t.Parallel()
	dir := restoreDir(t, map[string]string{"a.txt": "alpha"})
	sum := sha256.Sum256([]byte("alpha"))
	good := hex.EncodeToString(sum[:])
	env := CheckEnv{RestoreDir: dir}
	ctx := context.Background()

	if ev, _ := (HashMatch{}).Run(ctx, env); ev.Status != Pass {
		t.Errorf("empty manifest (engine-verified): %s, want pass", ev.Status)
	}
	if ev, _ := (HashMatch{Manifest: map[string]string{"a.txt": good}}).Run(ctx, env); ev.Status != Pass {
		t.Errorf("matching manifest: %s, want pass", ev.Status)
	}
	if ev, _ := (HashMatch{Manifest: map[string]string{"a.txt": "deadbeef"}}).Run(ctx, env); ev.Status != Fail {
		t.Errorf("mismatched manifest: %s, want fail", ev.Status)
	}
	if ev, _ := (HashMatch{Manifest: map[string]string{"missing.txt": good}}).Run(ctx, env); ev.Status != Error {
		t.Errorf("missing file: %s, want error", ev.Status)
	}
}

// A restored dangling symlink (its target is missing) must read as not-present
// for path_exists — "looks present but isn't". path_absent currently follows the
// link and reports absent; that stat-follow behavior is a tracked backlog item.
func TestPathExistsDanglingSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), filepath.Join(dir, "config.php")); err != nil {
		t.Fatal(err)
	}
	env := CheckEnv{RestoreDir: dir, Now: now}
	if ev, _ := (PathExists{Path: "config.php"}).Run(context.Background(), env); ev.Status != Fail {
		t.Errorf("path_exists on a dangling symlink: %s, want fail (target missing)", ev.Status)
	}
}
