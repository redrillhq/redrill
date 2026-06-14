//go:build integration || sabotage

package orchestrate

import (
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	exe "github.com/alyamovsky/redrill/internal/exec"
	"github.com/alyamovsky/redrill/internal/sandbox/docker"
)

// Shared real-engine scaffolding for the integration happy-path drills and the
// sabotage fixtures. Built under either tag so both suites can use it.

// --- borg ---

func requireBorg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("borg"); err != nil {
		t.Skip("borg not installed; provide borg to run engine fixtures")
	}
}

func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runBorgSetup(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("borg", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("borg %v: %v\n%s", args, err, out)
	}
}

// buildBorgRepo creates a repo with a config/ tree and, unless omitData, a data/
// tree (omitting it models missing-data-dir, a bad exclude). archiveAge>0
// backdates the archive via --timestamp (stale-source).
func buildBorgRepo(t *testing.T, omitData bool, archiveAge time.Duration) (repo, passFile string) {
	t.Helper()
	dir := t.TempDir()
	repo = filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	mkdirAll(t, filepath.Join(src, "config"))
	writeFile(t, filepath.Join(src, "config", "config.php"), "<?php // fixture")
	if !omitData {
		mkdirAll(t, filepath.Join(src, "data", "docs"))
		writeFile(t, filepath.Join(src, "data", "docs", "a.txt"), strings.Repeat("payload\n", 200))
	}
	passFile = filepath.Join(dir, "pass")
	writeFile(t, passFile, "testpass")

	env := append(os.Environ(), "BORG_PASSPHRASE=testpass")
	runBorgSetup(t, dir, env, "init", "--encryption=repokey", repo)
	args := []string{"create"}
	if archiveAge > 0 {
		args = append(args, "--timestamp", time.Now().UTC().Add(-archiveAge).Format("2006-01-02T15:04:05"))
	}
	args = append(args, repo+"::arch1", ".")
	runBorgSetup(t, src, env, args...)
	return repo, passFile
}

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

// sqlDumpdir writes body as a gzipped SQL dump in a fresh dumpdir.
func sqlDumpdir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "app.sql.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
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
