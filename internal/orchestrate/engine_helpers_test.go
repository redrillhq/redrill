//go:build integration || sabotage

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	exe "github.com/alyamovsky/redrill/internal/exec"
	"github.com/alyamovsky/redrill/internal/sandbox/docker"
)

// Shared real-engine scaffolding for the integration happy-path drills and the
// sabotage fixtures. The backups themselves are built by internal/fixtures; this
// file holds drill construction, the run helpers, and runtime gating.

// --- borg ---

func borgSource(repo, passFile string) config.Source {
	return config.Source{Name: "borg1", Type: "borg", Repo: repo, PassphraseFile: passFile}
}

// borgL1L2Drill: native check + a lax recency window + an L2 sample restore that
// asserts both config/ and data/ are present (so missing-data-dir fails at L2).
func borgL1L2Drill() config.Drill {
	nc := true
	sma := config.Duration(365 * 24 * time.Hour)
	return config.Drill{Name: "nc", Source: "borg1", Levels: config.Levels{
		L1: &config.L1{NativeCheck: &nc, SnapshotMaxAge: &sma},
		L2: &config.L2{
			Restore: config.Restore{Scope: "sample", IncludePaths: []string{"config/", "data/"}},
			Checks: []config.Check{
				{Kind: "path_exists", Path: "config/config.php"},
				{Kind: "path_exists", Path: "data/docs/a.txt"},
				{Kind: "hash_match", HashMatch: true},
			},
		},
	}}
}

// borgStaleDrill: a strict snapshot_max_age so a backdated archive fails L1.
func borgStaleDrill() config.Drill {
	nc := true
	sma := config.Duration(36 * time.Hour)
	return config.Drill{Name: "nc", Source: "borg1", Levels: config.Levels{
		L1: &config.L1{NativeCheck: &nc, SnapshotMaxAge: &sma},
	}}
}

// borgNativeOnlyDrill: just the native check, for the truncated-segment proof.
func borgNativeOnlyDrill() config.Drill {
	nc := true
	return config.Drill{Name: "nc", Source: "borg1", Levels: config.Levels{
		L1: &config.L1{NativeCheck: &nc},
	}}
}

func runBorgDrill(t *testing.T, repo, passFile string, drill config.Drill) RunResult {
	t.Helper()
	st := newStore(t)
	o := New(st, exe.NewLocal("testhost"), func() time.Time { return time.Now().UTC() })
	res, err := o.Run(context.Background(), drill, borgSource(repo, passFile),
		RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
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
		t.Fatalf("find segment under %s: %v", root, err)
	}
	return biggest
}

// --- dumpdir + docker L3 ---

func requireDocker(t *testing.T) *docker.Runtime {
	t.Helper()
	rt, err := docker.NewRuntime(context.Background())
	if err != nil {
		t.Skipf("no docker runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func runL3Drill(t *testing.T, rt *docker.Runtime, dir, image string, cfgChecks []config.Check) RunResult {
	t.Helper()
	st := newStore(t)
	src := config.Source{Name: "dumps", Type: "dumpdir", Path: dir, Pattern: "*.sql.gz", Pick: "newest"}
	drill := config.Drill{Name: "app-db", Source: "dumps", Levels: config.Levels{
		L3: &config.L3{Sandbox: config.Sandbox{Image: image, Memory: config.Size(1 << 30)}, Checks: cfgChecks},
	}}
	o := New(st, exe.NewLocal("test").WithSandbox(rt), func() time.Time { return time.Now().UTC() })
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	res, err := o.Run(ctx, drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func runBorgL3Drill(t *testing.T, rt *docker.Runtime, repo, passFile, extractPath, image string, cfgChecks []config.Check) RunResult {
	t.Helper()
	st := newStore(t)
	drill := config.Drill{Name: "nc-db", Source: "borg1", Levels: config.Levels{
		L3: &config.L3{
			ExtractPath: extractPath,
			Sandbox:     config.Sandbox{Image: image, Memory: config.Size(1 << 30)},
			Checks:      cfgChecks,
		},
	}}
	o := New(st, exe.NewLocal("test").WithSandbox(rt), func() time.Time { return time.Now().UTC() })
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	res, err := o.Run(ctx, drill, borgSource(repo, passFile), RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}
