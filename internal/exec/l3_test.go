// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/redrillhq/redrill/internal/checks"
	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/redact"
	"github.com/redrillhq/redrill/internal/sandbox"
)

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

func pgRoute(sqlOut string, sqlExit int) func([]string) sandbox.ExecResult {
	return func(cmd []string) sandbox.ExecResult {
		j := strings.Join(cmd, " ")
		switch {
		case strings.Contains(j, "pg_database"):
			return sandbox.ExecResult{ExitCode: 0} // no extra db → "postgres"
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

// Dump loads but the key table is empty → sql count fails.
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

// A dump from a newer pg major is caught before the sandbox starts.
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

// No sandbox runtime → ErrNoSandboxRuntime (skipped), never a silent pass.
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

// A dump that decompresses past the quota is refused as error before the sandbox
// starts, never a silent fill.
func TestRunDumpdirL3QuotaExceeded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app.sql.gz", strings.Repeat("SELECT 1;\n", 1000), base) // ~10 KB
	rt := &fakeRuntime{sb: &fakeSandbox{exec: pgRoute("42", 0)}}
	step := dumpdirL3Step(dir, t.TempDir(), "postgres:16", []config.Check{sqlCheck("select count(*) from users", "> 0")})
	step.Scratch.MaxBytes = config.Size(64) // smaller than the dump

	res, err := NewLocal("h").WithSandbox(rt).RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Error {
		t.Fatalf("status = %s, want error (quota); summary = %q", res.Status, res.Summary)
	}
	if !strings.Contains(res.Summary, "quota") {
		t.Errorf("summary = %q, want it to name the quota", res.Summary)
	}
	if rt.sb.closed {
		t.Error("sandbox should not have started once staging hit the quota")
	}
}

func dbSandbox(dbs ...string) *fakeSandbox {
	out := strings.Join(dbs, "\n")
	return &fakeSandbox{exec: func([]string) sandbox.ExecResult {
		return sandbox.ExecResult{Stdout: out, ExitCode: 0}
	}}
}

func TestLoadedDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Dump created its own database 'restored' (absent before the load).
	if db := loadedDB(ctx, dbSandbox("postgres", "restored"), map[string]bool{"postgres": true}); db != "restored" {
		t.Errorf("loadedDB = %q, want restored (the dump's database)", db)
	}
	// POSTGRES_DB pre-created 'app' but the dump loaded into postgres: target postgres.
	before := map[string]bool{"postgres": true, "app": true}
	if db := loadedDB(ctx, dbSandbox("postgres", "app"), before); db != "postgres" {
		t.Errorf("loadedDB = %q, want postgres (app pre-existed, not the dump's)", db)
	}
	// Plain dump into postgres, nothing created.
	if db := loadedDB(ctx, dbSandbox("postgres"), map[string]bool{"postgres": true}); db != "postgres" {
		t.Errorf("loadedDB = %q, want postgres", db)
	}
}

func TestPgMajor(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"postgres:16":                           16,
		"postgres:16.2":                         16,
		"postgres:16-bookworm":                  16,
		"registry.example.com:5000/postgres:16": 16, // port not read as tag
		"registry:5000/library/postgres:16":     16,
		"postgres":                              0, // no tag
		"postgres:latest":                       0, // non-numeric tag
		"registry:5000/postgres":                0, // port, no tag
		"postgres@sha256:abc123":                0, // digest pin
	}
	for image, want := range cases {
		if got := pgMajor(image); got != want {
			t.Errorf("pgMajor(%q) = %d, want %d", image, got, want)
		}
	}
}

func TestResolveLoader(t *testing.T) {
	t.Parallel()
	cases := []struct{ load, format, want string }{
		{"auto", "custom", "pg_restore"},
		{"auto", "plain", "psql"},
		{"", "custom", "pg_restore"},
		{"pg_restore", "plain", "pg_restore"}, // explicit override wins
		{"psql", "custom", "psql"},
	}
	for _, c := range cases {
		if got := resolveLoader(c.load, c.format); got != c.want {
			t.Errorf("resolveLoader(%q, %q) = %q, want %q", c.load, c.format, got, c.want)
		}
	}
}

// The version-trap result routes evidence and summary through the redactor.
func TestVersionTrapResultRedacts(t *testing.T) {
	t.Parallel()
	red := redact.New()
	red.AddSecret("s3cr3t")
	out := versionTrapResult(StepResult{Level: "l3"}, 99, 16, "/tmp/s3cr3t.dump", red)
	if strings.Contains(out.Summary, "s3cr3t") {
		t.Errorf("summary leaked the secret: %q", out.Summary)
	}
	for _, ev := range out.Evidence {
		if strings.Contains(ev.Target, "s3cr3t") || strings.Contains(ev.Actual, "s3cr3t") {
			t.Errorf("evidence leaked the secret: %+v", ev)
		}
	}
}
