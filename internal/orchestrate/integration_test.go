//go:build integration

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
	"github.com/alyamovsky/redrill/internal/store"
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

// --- L3 postgres sandbox (real Docker) ---

func requireDocker(t *testing.T) *docker.Runtime {
	t.Helper()
	rt, err := docker.NewRuntime(context.Background())
	if err != nil {
		t.Skipf("no docker runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// sqlDumpdir writes body as a gzipped plain-SQL "dump" in a fresh dumpdir.
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

func TestIntegrationDumpdirL3(t *testing.T) {
	rt := requireDocker(t)
	dir := sqlDumpdir(t, "CREATE TABLE users(id int);\nINSERT INTO users VALUES (1),(2),(3);\n")
	res := runL3Drill(t, rt, dir, "postgres:16", []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
		{Kind: "sql_no_error", SQLNoError: "select * from users limit 1"},
	})
	if res.Status != store.ResultPass {
		t.Fatalf("dumpdir L3 = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
}

// wrong-db-dump: the dump loads but the key table is empty → sql count fails.
func TestIntegrationWrongDBDump(t *testing.T) {
	rt := requireDocker(t)
	dir := sqlDumpdir(t, "CREATE TABLE users(id int);\n")
	res := runL3Drill(t, rt, dir, "postgres:16", []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
	})
	if res.Status != store.ResultFail {
		t.Fatalf("wrong-db-dump NOT caught: %s, want fail; levels = %+v", res.Status, res.Levels)
	}
}

// version-trap: a dump from a newer pg major than the sandbox is refused.
func TestIntegrationVersionTrap(t *testing.T) {
	rt := requireDocker(t)
	dir := sqlDumpdir(t, "-- Dumped from database version 99.0\nCREATE TABLE users(id int);\nINSERT INTO users VALUES (1);\n")
	res := runL3Drill(t, rt, dir, "postgres:16", []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
	})
	if res.Status != store.ResultFail {
		t.Fatalf("version-trap NOT caught: %s, want fail; levels = %+v", res.Status, res.Levels)
	}
}
