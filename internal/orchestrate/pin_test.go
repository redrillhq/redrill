// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package orchestrate

import (
	"context"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/checks"
	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/exec"
	"github.com/redrillhq/redrill/internal/store"
)

// pinExec records each step's Snapshot pin and reports one back from the
// first level, like a real executor resolving "newest".
type pinExec struct {
	pins []string
}

func (p *pinExec) Describe() exec.ExecutorInfo { return exec.ExecutorInfo{Host: "fake"} }

func (p *pinExec) RunStep(_ context.Context, step exec.StepSpec) (exec.StepResult, error) {
	p.pins = append(p.pins, step.Snapshot)
	res := exec.StepResult{Level: step.Level, Status: checks.Pass, Summary: "ok", Snapshot: "snap-A"}
	if step.Keep && step.Level == "l3" {
		res.KeptSandbox = "sb-kept-1"
	}
	return res, nil
}

// Every level of a run must audit the same snapshot: the first level's
// resolution pins the rest (the mid-run-backup TOCTOU from the 2026-07-03
// architecture assessment, #5).
func TestRunPinsSnapshotAcrossLevels(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	pe := &pinExec{}
	o := New(st, pe, func() time.Time { return base })

	size := config.Size(1)
	drill := config.Drill{
		Name: "d", Source: "s",
		Levels: config.Levels{
			L1: &config.L1{FileMinBytes: &size},
			L2: &config.L2{Restore: config.Restore{Scope: "full"}},
			L3: &config.L3{Sandbox: config.Sandbox{Image: "postgres:16"}, Checks: []config.Check{{Kind: "sql_no_error", SQLNoError: "select 1"}}},
		},
	}
	src := config.Source{Name: "s", Type: "dumpdir", Path: t.TempDir(), Pattern: "*.gz"}

	res, err := o.Run(context.Background(), drill, src, RunOptions{Trigger: store.TriggerManual, Keep: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != store.ResultPass {
		t.Fatalf("status = %s, want pass", res.Status)
	}
	if res.KeptSandbox != "sb-kept-1" {
		t.Errorf("KeptSandbox = %q, want the executor-reported id threaded through", res.KeptSandbox)
	}
	want := []string{"", "snap-A", "snap-A"}
	if len(pe.pins) != len(want) {
		t.Fatalf("steps = %d (%v), want %d", len(pe.pins), pe.pins, len(want))
	}
	for i, w := range want {
		if pe.pins[i] != w {
			t.Errorf("step %d pin = %q, want %q (a run must audit one snapshot)", i, pe.pins[i], w)
		}
	}

	// The audited snapshot is recorded on the run row, so evidence still names
	// the backup it tested after the source rotates.
	run, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Snapshot != "snap-A" {
		t.Errorf("runs.snapshot = %q, want snap-A", run.Snapshot)
	}
}
