//go:build integration

package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alyamovsky/drillbit/internal/sandbox"
)

func newRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := NewRuntime(context.Background())
	if err != nil {
		t.Skipf("no docker runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func TestIntegrationSandboxLifecycle(t *testing.T) {
	rt := newRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dump := filepath.Join(t.TempDir(), "probe.sql")
	if err := os.WriteFile(dump, []byte("select 'injected';"), 0o600); err != nil {
		t.Fatal(err)
	}

	sb, err := rt.Start(ctx, sandbox.SandboxSpec{
		Image:    "postgres:16",
		Env:      map[string]string{"POSTGRES_PASSWORD": "drill"},
		Network:  "none",
		Memory:   1 << 30,
		Labels:   map[string]string{sandbox.RunLabel: "test-lifecycle"},
		ReadyCmd: []string{"pg_isready", "-U", "postgres"},
		Files:    []sandbox.FileInject{{HostPath: dump, ContainerPath: "/tmp/probe.sql"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sb.Close(ctx) }()

	res, err := sb.Exec(ctx, []string{"psql", "-U", "postgres", "-tAc", "select 1+1"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "2" {
		t.Errorf("select 1+1 = %q (exit %d, stderr %q)", res.Stdout, res.ExitCode, res.Stderr)
	}

	cat, err := sb.Exec(ctx, []string{"cat", "/tmp/probe.sql"})
	if err != nil || !strings.Contains(cat.Stdout, "injected") {
		t.Errorf("injected file = %q (%v)", cat.Stdout, err)
	}

	// A bad query exits non-zero (so the sql checks can tell pass from fail).
	bad, err := sb.Exec(ctx, []string{"psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-tAc", "select * from nope"})
	if err != nil {
		t.Fatalf("Exec(bad): %v", err)
	}
	if bad.ExitCode == 0 {
		t.Errorf("a failing query should exit non-zero, got 0 (stderr %q)", bad.Stderr)
	}

	if err := sb.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := sb.Close(ctx); err != nil {
		t.Errorf("Close is not idempotent: %v", err)
	}
}

func TestIntegrationJanitor(t *testing.T) {
	rt := newRuntime(t)
	ctx := context.Background()

	sb, err := rt.Start(ctx, sandbox.SandboxSpec{
		Image:  "postgres:16",
		Env:    map[string]string{"POSTGRES_PASSWORD": "drill"},
		Labels: map[string]string{sandbox.RunLabel: "orphan"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sb.Close(ctx) }() // in case the janitor doesn't get it

	n, err := rt.Janitor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("janitor removed %d labeled containers, want >= 1", n)
	}
}

// A container that exits during boot (postgres with no password / trust auth)
// must fail readiness fast — Start returns promptly with a "container exited"
// error instead of polling pg_isready until the context deadline.
func TestIntegrationReadyFailsFastOnExit(t *testing.T) {
	rt := newRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	sb, err := rt.Start(ctx, sandbox.SandboxSpec{
		Image:    "postgres:16", // no POSTGRES_PASSWORD/HOST_AUTH_METHOD → entrypoint exits non-zero
		Network:  "none",
		Labels:   map[string]string{sandbox.RunLabel: "fail-fast"},
		ReadyCmd: []string{"pg_isready", "-U", "postgres"},
	})
	if sb != nil {
		_ = sb.Close(ctx)
	}
	if err == nil {
		t.Fatal("Start: want an error for a container that exits during init")
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("err = %v, want it to name the container exit (fail-fast, not a deadline)", err)
	}
	if d := time.Since(start); d > 60*time.Second {
		t.Errorf("Start took %s — waitReady likely polled to the deadline instead of failing fast", d)
	}
}
