package exec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alyamovsky/drillbit/internal/checks"
	"github.com/alyamovsky/drillbit/internal/config"
	"github.com/alyamovsky/drillbit/internal/sandbox"
)

// fakeSandbox routes container Exec calls to canned results so the L3 glue is
// unit-testable without Docker.
type fakeSandbox struct {
	spec   sandbox.SandboxSpec
	exec   func(cmd []string) sandbox.ExecResult
	closed bool
}

func (s *fakeSandbox) Endpoint(string) (string, error) { return "", nil }
func (s *fakeSandbox) Exec(_ context.Context, cmd []string) (sandbox.ExecResult, error) {
	return s.exec(cmd), nil
}
func (s *fakeSandbox) Close(context.Context) error { s.closed = true; return nil }

type fakeRuntime struct {
	sb       *fakeSandbox
	startErr error
}

func (r *fakeRuntime) Start(_ context.Context, spec sandbox.SandboxSpec) (sandbox.Sandbox, error) {
	if r.startErr != nil {
		return nil, r.startErr
	}
	r.sb.spec = spec
	return r.sb, nil
}

// pgRoute answers the load, the target-DB probe, and a scalar query.
func pgRoute(sqlOut string, sqlExit int) func([]string) sandbox.ExecResult {
	return func(cmd []string) sandbox.ExecResult {
		j := strings.Join(cmd, " ")
		switch {
		case strings.Contains(j, "pg_database"):
			return sandbox.ExecResult{ExitCode: 0} // no extra db -> "postgres"
		case strings.Contains(j, containerDumpPath):
			return sandbox.ExecResult{ExitCode: 0} // load ok
		default:
			return sandbox.ExecResult{Stdout: sqlOut, ExitCode: sqlExit}
		}
	}
}

func dumpdirL3Step(dir, scratchDir, image string, cfgChecks []config.Check) StepSpec {
	mem := config.Size(1 << 30)
	return StepSpec{
		RunID:   1,
		Level:   "l3",
		Source:  config.Source{Type: "dumpdir", Path: dir, Pattern: "*.sql.gz", Pick: "newest"},
		L3:      &config.L3{Sandbox: config.Sandbox{Image: image, Memory: mem}, Checks: cfgChecks},
		Scratch: config.Scratch{Dir: scratchDir},
		Now:     base,
	}
}

func sqlCheck(query, expect string) config.Check {
	return config.Check{Kind: "sql", SQL: &config.SQLCheck{Query: query, Expect: expect}}
}

func TestRunDumpdirL3Pass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app.sql.gz", "-- a dump\nSELECT 1;\n", base)
	rt := &fakeRuntime{sb: &fakeSandbox{exec: pgRoute("42", 0)}}
	step := dumpdirL3Step(dir, t.TempDir(), "postgres:16", []config.Check{sqlCheck("select count(*) from users", "> 0")})

	res, err := NewLocal("h").WithSandbox(rt).RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s, want pass (%s)", res.Status, res.Summary)
	}
	if rt.sb.spec.Network != "none" || rt.sb.spec.Labels[sandbox.RunLabel] != "1" {
		t.Errorf("sandbox spec missing network=none/run label: %+v", rt.sb.spec)
	}
	if !rt.sb.closed {
		t.Error("sandbox not closed")
	}
}

// wrong-db-dump: the dump loads but the key table is empty → sql count fails.
func TestRunDumpdirL3WrongDBFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app.sql.gz", "-- a dump\nSELECT 1;\n", base)
	rt := &fakeRuntime{sb: &fakeSandbox{exec: pgRoute("0", 0)}}
	step := dumpdirL3Step(dir, t.TempDir(), "postgres:16", []config.Check{sqlCheck("select count(*) from users", "> 0")})

	res, _ := NewLocal("h").WithSandbox(rt).RunStep(context.Background(), step)
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail (0 rows)", res.Status)
	}
}

// version-trap: a dump from a newer pg major is caught before the sandbox even
// starts.
func TestRunDumpdirL3VersionTrap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app.sql.gz", "-- Dumped from database version 99.1\nSELECT 1;\n", base)
	rt := &fakeRuntime{sb: &fakeSandbox{exec: pgRoute("42", 0)}}
	step := dumpdirL3Step(dir, t.TempDir(), "postgres:16", []config.Check{sqlCheck("select count(*) from users", "> 0")})

	res, _ := NewLocal("h").WithSandbox(rt).RunStep(context.Background(), step)
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail (version trap)", res.Status)
	}
	if !strings.Contains(res.Summary, "version trap") {
		t.Errorf("summary = %q, want it to name the version trap", res.Summary)
	}
	if rt.sb.closed {
		t.Error("sandbox should not have started for a version trap")
	}
}

// No sandbox runtime → L3 is skipped (the orchestrator's job), signaled by
// ErrNoSandboxRuntime — never a silent pass.
func TestRunL3NoRuntimeSkips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app.sql.gz", "SELECT 1;\n", base)
	step := dumpdirL3Step(dir, t.TempDir(), "postgres:16", []config.Check{sqlCheck("select 1", "> 0")})

	_, err := NewLocal("h").RunStep(context.Background(), step) // no WithSandbox
	if !errors.Is(err, ErrNoSandboxRuntime) {
		t.Fatalf("err = %v, want ErrNoSandboxRuntime", err)
	}
}
