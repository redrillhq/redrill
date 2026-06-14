package borg

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alyamovsky/redrill/internal/driver"
)

type fakeRunner struct {
	calls  [][]string // each is [binary, subcommand, ...args]
	envs   [][]string
	stdout map[string][]byte // keyed by subcommand
	exit   map[string]int
}

func newFake() *fakeRunner {
	return &fakeRunner{stdout: map[string][]byte{}, exit: map[string]int{}}
}

func (f *fakeRunner) run(_ context.Context, _ string, env []string, name string, args []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	return f.stdout[sub], nil, f.exit[sub], nil
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The load-bearing invariant: the borg driver is read-only on the repository.
// Exercise every method and assert each invocation uses an allow-listed
// read-only subcommand — never create/prune/delete/compact/init/etc.
func TestOnlyReadOnlyCommands(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.stdout["list"] = read(t, "list.json")
	f.stdout["info"] = read(t, "info-archive.json")
	d := New("/repo", WithRunner(f.run), WithPassphrase("p"))
	ctx := context.Background()

	_ = d.Validate(ctx)
	_, _ = d.ListSnapshots(ctx)
	_, _ = d.ListFiles(ctx, "arch-2")
	_, _ = d.NativeCheck(ctx, driver.NativeCheckOpts{})
	_, _ = d.Restore(ctx, driver.Selection{SnapshotIDs: []string{"arch-2"}, Paths: []string{"config/"}}, t.TempDir())
	_, _ = d.ArchiveSize(ctx, "arch-2")

	if len(f.calls) == 0 {
		t.Fatal("no borg invocations recorded")
	}
	allowed := map[string]bool{"list": true, "info": true, "check": true, "extract": true}
	for _, call := range f.calls {
		if len(call) < 2 {
			t.Fatalf("malformed call %v", call)
		}
		if sub := call[1]; !allowed[sub] {
			t.Errorf("borg %q is not a read-only subcommand (call: %v)", sub, call)
		}
	}
}

func TestParseListNewestFirst(t *testing.T) {
	t.Parallel()
	snaps, err := parseList(read(t, "list.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d archives, want 2", len(snaps))
	}
	if snaps[0].ID != "arch-2" || snaps[1].ID != "arch-1" {
		t.Errorf("order = %s,%s, want newest-first arch-2,arch-1", snaps[0].ID, snaps[1].ID)
	}
	// Time is parsed in local time, so the wall clock matches the string regardless of TZ.
	got := snaps[0].Time
	if got.Hour() != 18 || got.Minute() != 20 || got.Second() != 51 {
		t.Errorf("arch-2 time = %v, want 18:20:51 wall clock", got)
	}
}

func TestParseFiles(t *testing.T) {
	t.Parallel()
	files, err := parseFiles(read(t, "list-files.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	regs := 0
	var cfg driver.FileEntry
	for _, f := range files {
		if f.IsFile {
			regs++
		}
		if f.Path == "config/config.php" {
			cfg = f
		}
	}
	if len(files) != 7 || regs != 3 {
		t.Fatalf("entries=%d regular=%d, want 7/3 (4 dirs + 3 files)", len(files), regs)
	}
	if !cfg.IsFile || cfg.Size != 4 {
		t.Errorf("config/config.php entry = %+v, want regular file of 4 bytes", cfg)
	}
}

func TestParseArchiveSize(t *testing.T) {
	t.Parallel()
	n, err := parseArchiveSize(read(t, "info-archive.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Errorf("original_size = %d, want 12", n)
	}
}

// borg check: exit 0 clean, exit 1 errors-found (fail, not Go error), exit ≥2
// operational (Go error). This is what makes the truncated-segment fixture a
// fail, not an error.
func TestNativeCheckExitMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		exit    int
		wantOK  bool
		wantErr bool
	}{
		{0, true, false},
		{1, false, false},
		{2, false, true},
	}
	for _, tc := range cases {
		f := newFake()
		f.exit["check"] = tc.exit
		rep, err := New("/r", WithRunner(f.run)).NativeCheck(context.Background(), driver.NativeCheckOpts{})
		if tc.wantErr {
			if err == nil {
				t.Errorf("exit %d: want Go error", tc.exit)
			}
			continue
		}
		if err != nil {
			t.Errorf("exit %d: unexpected err %v", tc.exit, err)
		}
		if rep.OK != tc.wantOK {
			t.Errorf("exit %d: OK = %v, want %v", tc.exit, rep.OK, tc.wantOK)
		}
	}
}

func TestSecretEnvWiringNeverOnArgv(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.stdout["list"] = read(t, "list.json")
	d := New("/r", WithRunner(f.run), WithPassphrase("hunter2"), WithSSHKey("/keys/id_ed25519"))
	_, _ = d.ListSnapshots(context.Background())

	env := strings.Join(f.envs[0], "\n")
	if !strings.Contains(env, "BORG_PASSPHRASE=hunter2") {
		t.Error("BORG_PASSPHRASE not set in env")
	}
	if !strings.Contains(env, "BORG_RSH=ssh -i /keys/id_ed25519 -o BatchMode=yes") {
		t.Errorf("BORG_RSH not wired: %q", env)
	}
	for _, call := range f.calls {
		for _, arg := range call {
			if strings.Contains(arg, "hunter2") {
				t.Errorf("passphrase leaked onto argv: %v", call)
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
	bad.exit["list"] = 2
	if err := New("/r", WithRunner(bad.run)).Validate(context.Background()); err == nil {
		t.Error("exit 2: want error")
	}
}

func TestBinaryOverride(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.stdout["list"] = read(t, "list.json")
	_, _ = New("/r", WithRunner(f.run), WithBinary("/opt/borg")).ListSnapshots(context.Background())
	if f.calls[0][0] != "/opt/borg" {
		t.Errorf("binary = %q, want /opt/borg", f.calls[0][0])
	}
}

func TestCapabilities(t *testing.T) {
	t.Parallel()
	d := New("/r")
	if d.Name() != "borg" {
		t.Errorf("Name = %q", d.Name())
	}
	c := d.Capabilities()
	if !c.NativeCheck || !c.ListSnapshots || !c.PartialRestore {
		t.Errorf("caps = %+v", c)
	}
}
