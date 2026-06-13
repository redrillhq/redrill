//go:build integration

package orchestrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alyamovsky/drillbit/internal/config"
	exe "github.com/alyamovsky/drillbit/internal/exec"
	"github.com/alyamovsky/drillbit/internal/store"
)

// Full L1+L2 borg drill against a real repo built in test setup (TESTING.md),
// plus the missing-data-dir sabotage fixture caught at L2. Skipped where borg is
// absent; verified against real borg in a golang+borgbackup container.

func requireBorg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("borg"); err != nil {
		t.Skip("borg not installed; run via make test-integration with borg present")
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
// tree — omitting it models the missing-data-dir failure (a bad exclude).
func buildBorgRepo(t *testing.T, omitData bool) (repo, passFile string) {
	t.Helper()
	dir := t.TempDir()
	repo = filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	mkdirAll(t, filepath.Join(src, "config"))
	writeFile(t, filepath.Join(src, "config", "config.php"), "<?php // fixture")
	if !omitData {
		mkdirAll(t, filepath.Join(src, "data", "docs"))
		writeFile(t, filepath.Join(src, "data", "docs", "a.txt"), strings.Repeat("payload\n", 100))
	}
	passFile = filepath.Join(dir, "pass")
	writeFile(t, passFile, "testpass")

	env := append(os.Environ(), "BORG_PASSPHRASE=testpass")
	runBorgSetup(t, dir, env, "init", "--encryption=repokey", repo)
	runBorgSetup(t, src, env, "create", repo+"::arch1", ".")
	return repo, passFile
}

func borgDrill(repo, passFile string) (config.Drill, config.Source) {
	src := config.Source{Name: "borg1", Type: "borg", Repo: repo, PassphraseFile: passFile}
	nc := true
	sma := config.Duration(365 * 24 * time.Hour)
	drill := config.Drill{Name: "nc", Source: "borg1", Levels: config.Levels{
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
	return drill, src
}

func runBorgDrill(t *testing.T, repo, passFile string) RunResult {
	t.Helper()
	st := newStore(t)
	drill, src := borgDrill(repo, passFile)
	o := New(st, exe.NewLocal("testhost"), func() time.Time { return time.Now().UTC() })
	res, err := o.Run(context.Background(), drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestIntegrationBorgL1L2(t *testing.T) {
	requireBorg(t)
	repo, passFile := buildBorgRepo(t, false)
	res := runBorgDrill(t, repo, passFile)
	if res.Status != store.ResultPass {
		t.Fatalf("L1+L2 borg drill = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
	if res.LevelReached != "l2" {
		t.Errorf("level reached %s, want l2", res.LevelReached)
	}
}

// missing-data-dir: the archive lacks the data/ directory. L2 path_exists must
// catch it as fail.
func TestIntegrationMissingDataDir(t *testing.T) {
	requireBorg(t)
	repo, passFile := buildBorgRepo(t, true)
	res := runBorgDrill(t, repo, passFile)
	if res.Status != store.ResultFail {
		t.Fatalf("missing-data-dir NOT caught: result = %s, want fail; levels = %+v", res.Status, res.Levels)
	}
}
