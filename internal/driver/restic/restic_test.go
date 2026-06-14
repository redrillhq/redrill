package restic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alyamovsky/redrill/internal/driver"
)

type fakeRunner struct {
	calls  [][]string // each is [binary, ...args]
	envs   [][]string
	stdout map[string][]byte // keyed by subcommand
	exit   map[string]int
}

func newFake() *fakeRunner {
	return &fakeRunner{stdout: map[string][]byte{}, exit: map[string]int{}}
}

// run records the call and, for restore, materializes each --include path under
// --target (as real restic does: full stored path under target) so the
// root-strip promotion has real files to move.
func (f *fakeRunner) run(_ context.Context, _ string, env []string, name string, args []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	sub := subcommand(args)
	if sub == "restore" {
		if target := flagValue(args, "--target"); target != "" {
			for _, inc := range allFlagValues(args, "--include") {
				p := filepath.Join(target, filepath.FromSlash(strings.TrimPrefix(inc, "/")))
				_ = os.MkdirAll(filepath.Dir(p), 0o700)
				_ = os.WriteFile(p, []byte("payload"), 0o600)
			}
		}
	}
	return f.stdout[sub], nil, f.exit[sub], nil
}

// subcommand returns the first non-flag, non-flag-value token after the global
// "-r <repo>" pair — restic's subcommand.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-r", "--target", "--include", "--limit-download", "--mode":
			i++ // skip the flag's value
		default:
			if !strings.HasPrefix(args[i], "-") {
				return args[i]
			}
		}
	}
	return ""
}

func flagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func allFlagValues(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOnlyReadOnlyCommands(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.stdout["snapshots"] = read(t, "snapshots.json")
	f.stdout["ls"] = read(t, "ls.json")
	f.stdout["stats"] = read(t, "stats.json")
	d := New("/repo", WithRunner(f.run), WithPassword("p"))
	ctx := context.Background()

	_ = d.Validate(ctx)
	_, _ = d.ListSnapshots(ctx)
	_, _ = d.ListFiles(ctx, "snap2")
	_, _ = d.NativeCheck(ctx, driver.NativeCheckOpts{})
	_, _ = d.Restore(ctx, driver.Selection{SnapshotIDs: []string{"snap2"}, Paths: []string{"config/config.php"}}, t.TempDir())
	_, _ = d.SnapshotSize(ctx, "snap2")

	if len(f.calls) == 0 {
		t.Fatal("no restic invocations recorded")
	}
	allowed := map[string]bool{"snapshots": true, "check": true, "ls": true, "restore": true, "stats": true}
	for _, call := range f.calls {
		sub := subcommand(call[1:])
		if !allowed[sub] {
			t.Errorf("restic %q is not a read-only subcommand (call: %v)", sub, call)
		}
	}
}

func TestParseSnapshotsNewestFirst(t *testing.T) {
	t.Parallel()
	snaps, err := parseSnapshots(read(t, "snapshots.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snaps))
	}
	if !strings.HasPrefix(snaps[0].ID, "f697") || !strings.HasPrefix(snaps[1].ID, "aaaa") {
		t.Errorf("order = %s,%s, want newest-first (f697…, aaaa…)", snaps[0].ID[:4], snaps[1].ID[:4])
	}
	if !snaps[0].Time.After(snaps[1].Time) {
		t.Errorf("not newest-first by time: %v then %v", snaps[0].Time, snaps[1].Time)
	}
	if snaps[0].Time.Location().String() != "UTC" {
		t.Errorf("time not normalized to UTC: %v", snaps[0].Time.Location())
	}
}

// ListFiles strips the snapshot's backup root so paths are relative (like borg).
func TestParseFilesRelative(t *testing.T) {
	t.Parallel()
	files, err := parseFiles(read(t, "ls.json"))
	if err != nil {
		t.Fatal(err)
	}
	regs := 0
	got := map[string]driver.FileEntry{}
	for _, f := range files {
		if f.IsFile {
			regs++
		}
		got[f.Path] = f
	}
	if regs != 3 {
		t.Errorf("regular files = %d, want 3", regs)
	}
	cfg, ok := got["config/config.php"]
	if !ok {
		t.Fatalf("config/config.php not found (root not stripped); paths = %v", keys(got))
	}
	if !cfg.IsFile || cfg.Size != 16 {
		t.Errorf("config/config.php = %+v, want regular file of 16 bytes", cfg)
	}
	if _, ok := got["data/docs/a.txt"]; !ok {
		t.Errorf("data/docs/a.txt not found; paths = %v", keys(got))
	}
}

func TestParseStatsSize(t *testing.T) {
	t.Parallel()
	n, err := parseStatsSize(read(t, "stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1631 {
		t.Errorf("total_size = %d, want 1631", n)
	}
}

// restic check: exit 0 clean, non-zero errors-found (fail, not Go error after
// Validate has proven reachability); a process that can't start is a Go error.
func TestNativeCheckExitMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		exit   int
		wantOK bool
	}{
		{0, true},
		{1, false},
		{2, false},
	}
	for _, tc := range cases {
		f := newFake()
		f.exit["check"] = tc.exit
		rep, err := New("/r", WithRunner(f.run)).NativeCheck(context.Background(), driver.NativeCheckOpts{})
		if err != nil {
			t.Errorf("exit %d: unexpected Go error %v", tc.exit, err)
		}
		if rep.OK != tc.wantOK {
			t.Errorf("exit %d: OK = %v, want %v", tc.exit, rep.OK, tc.wantOK)
		}
	}
}

func TestSecretEnvWiringNeverOnArgv(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.stdout["snapshots"] = read(t, "snapshots.json")
	d := New("/r", WithRunner(f.run),
		WithPassword("hunter2"),
		WithBackendEnv(map[string]string{"AWS_SECRET_ACCESS_KEY": "topsecret"}))
	_, _ = d.ListSnapshots(context.Background())

	env := strings.Join(f.envs[0], "\n")
	if !strings.Contains(env, "RESTIC_PASSWORD=hunter2") {
		t.Error("RESTIC_PASSWORD not set in env")
	}
	if !strings.Contains(env, "AWS_SECRET_ACCESS_KEY=topsecret") {
		t.Error("backend env not wired")
	}
	for _, call := range f.calls {
		for _, arg := range call {
			if strings.Contains(arg, "hunter2") || strings.Contains(arg, "topsecret") {
				t.Errorf("secret leaked onto argv: %v", call)
			}
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	ok := newFake()
	if err := New("/r", WithRunner(ok.run)).Validate(context.Background()); err != nil {
		t.Errorf("exit 0: %v", err)
	}
	bad := newFake()
	bad.exit["snapshots"] = 1
	if err := New("/r", WithRunner(bad.run)).Validate(context.Background()); err == nil {
		t.Error("exit 1: want error")
	}
}

func TestBinaryOverride(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.stdout["snapshots"] = read(t, "snapshots.json")
	_, _ = New("/r", WithRunner(f.run), WithBinary("/opt/restic")).ListSnapshots(context.Background())
	if f.calls[0][0] != "/opt/restic" {
		t.Errorf("binary = %q, want /opt/restic", f.calls[0][0])
	}
}

// bandwidth_limit maps to restic's own --limit-download; unset adds no flag.
func TestRestoreDownloadRateLimit(t *testing.T) {
	t.Parallel()
	t.Run("set", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.stdout["snapshots"] = read(t, "snapshots.json")
		d := New("/r", WithRunner(f.run), WithDownloadRateLimit(40960))
		_, _ = d.Restore(context.Background(), driver.Selection{SnapshotIDs: []string{"snap2"}}, t.TempDir())
		if !containsSeq(f.calls[len(f.calls)-1], "--limit-download", "40960") {
			t.Errorf("restore argv = %v, want --limit-download 40960", f.calls[len(f.calls)-1])
		}
	})
	t.Run("unset", func(t *testing.T) {
		t.Parallel()
		f := newFake()
		f.stdout["snapshots"] = read(t, "snapshots.json")
		d := New("/r", WithRunner(f.run))
		_, _ = d.Restore(context.Background(), driver.Selection{SnapshotIDs: []string{"snap2"}}, t.TempDir())
		for _, call := range f.calls {
			for _, a := range call {
				if a == "--limit-download" {
					t.Errorf("no ratelimit flag expected: %v", call)
				}
			}
		}
	})
}

// Restore strips the snapshot's backup root: a file stored at /src/config/config.php
// lands at <target>/config/config.php, and the staging dir is cleaned up.
func TestRestoreStripsRoot(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.stdout["snapshots"] = read(t, "snapshots.json")
	target := t.TempDir()
	rep, err := New("/r", WithRunner(f.run)).Restore(
		context.Background(),
		driver.Selection{SnapshotIDs: []string{"snap2"}, Paths: []string{"config/config.php"}},
		target,
	)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "config", "config.php")); err != nil {
		t.Errorf("root not stripped, file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".restic-stage")); !os.IsNotExist(err) {
		t.Errorf("staging dir not cleaned up")
	}
	if rep.Files != 1 {
		t.Errorf("report files = %d, want 1", rep.Files)
	}
	// The --include uses the full stored path (root re-prepended).
	if !containsSeq(f.calls[len(f.calls)-1], "--include", "/tmp/src/config/config.php") {
		t.Errorf("include not root-qualified: %v", f.calls[len(f.calls)-1])
	}
}

func TestJoinAndStripRoot(t *testing.T) {
	t.Parallel()
	if got := joinRoot("/src", "config/x"); got != "/src/config/x" {
		t.Errorf("joinRoot = %q", got)
	}
	if got := joinRoot("", "config/x"); got != "config/x" {
		t.Errorf("joinRoot no-root = %q", got)
	}
	if got := stripRoot("/src", "/src/config/x"); got != "config/x" {
		t.Errorf("stripRoot = %q", got)
	}
	if got := stripRoot("", "/abs/x"); got != "abs/x" {
		t.Errorf("stripRoot no-root = %q", got)
	}
}

func TestCapabilities(t *testing.T) {
	t.Parallel()
	d := New("/r")
	if d.Name() != "restic" {
		t.Errorf("Name = %q", d.Name())
	}
	c := d.Capabilities()
	if !c.NativeCheck || !c.ListSnapshots || !c.PartialRestore {
		t.Errorf("caps = %+v", c)
	}
	if c.HashManifest {
		t.Error("restic exposes no per-file hash manifest; HashManifest should be false")
	}
}

func containsSeq(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func keys(m map[string]driver.FileEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
