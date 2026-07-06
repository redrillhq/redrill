// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/redrillhq/redrill/internal/checks"
	"github.com/redrillhq/redrill/internal/config"
)

// Keep leaves the sandbox running and reports its id for the exec hints.
func TestKeepLeavesSandboxRunning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app.sql.gz", "-- a dump\nSELECT 1;\n", base)
	rt := &fakeRuntime{sb: &fakeSandbox{exec: pgRoute("42", 0), id: "cafe1234beef"}}
	step := dumpdirL3Step(dir, t.TempDir(), "postgres:16", []config.Check{sqlCheck("select count(*) from users", "> 0")})
	step.Keep = true

	res, err := NewLocal("h").WithSandbox(rt).RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s, want pass (%s)", res.Status, res.Summary)
	}
	if rt.sb.closed {
		t.Error("sandbox closed despite Keep")
	}
	if res.KeptSandbox != "cafe1234beef" {
		t.Errorf("KeptSandbox = %q, want the container id", res.KeptSandbox)
	}
}

// Keep also applies when the level fails — a failed load is exactly what an
// operator wants to inspect.
func TestKeepLeavesSandboxOnFail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app.sql.gz", "-- a dump\nSELECT 1;\n", base)
	rt := &fakeRuntime{sb: &fakeSandbox{exec: pgRoute("0", 0), id: "dead0000beef"}} // count 0 → sql fail
	step := dumpdirL3Step(dir, t.TempDir(), "postgres:16", []config.Check{sqlCheck("select count(*) from users", "> 0")})
	step.Keep = true

	res, err := NewLocal("h").WithSandbox(rt).RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if rt.sb.closed || res.KeptSandbox == "" {
		t.Errorf("failed level must still keep: closed=%v kept=%q", rt.sb.closed, res.KeptSandbox)
	}
}

// Keep preserves the L2 scratch restore for inspection.
func TestKeepPreservesScratchL2(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app.sql.gz", "SELECT 1; -- a healthy dump", base)
	scratchDir := t.TempDir()
	step := dumpdirL2Step(dir, []config.Check{{Kind: "min_total_bytes", MinTotalBytes: config.Size(1)}}, scratchDir, 0)
	step.Keep = true

	res, err := NewLocal("h").RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s (%s), want pass", res.Status, res.Summary)
	}
	kept := filepath.Join(scratchDir, "run-1")
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("scratch %s not kept: %v", kept, err)
	}

	// Without Keep the same run cleans up after itself.
	step2 := dumpdirL2Step(dir, []config.Check{{Kind: "min_total_bytes", MinTotalBytes: config.Size(1)}}, t.TempDir(), 0)
	if _, err := NewLocal("h").RunStep(context.Background(), step2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(step2.Scratch.Dir, "run-1")); !os.IsNotExist(err) {
		t.Errorf("scratch survived without Keep (err=%v)", err)
	}
}
