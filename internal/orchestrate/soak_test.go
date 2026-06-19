// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package orchestrate

import (
	"context"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/exec"
	"github.com/redrillhq/redrill/internal/fixtures"
	"github.com/redrillhq/redrill/internal/scheduler"
	"github.com/redrillhq/redrill/internal/store"
)

// soakDrill is a dumpdir L1 drill with no freshness check, so it passes on every
// weekly run regardless of how far the injected clock advances.
func soakDrill(dir string, ret config.Retention) (config.Drill, config.Source) {
	fmb := config.Size(1)
	ct := true
	drill, src := drillFor(dir, config.Levels{L1: &config.L1{FileMinBytes: &fmb, CompressionTest: &ct}})
	drill.Schedule = "Sun 04:10"
	drill.MaxProofAge = config.Duration(10 * 24 * time.Hour)
	drill.Retention = ret
	return drill, src
}

// Time-compressed soak: drive weekly drills over an injected clock and assert
// count-retention prunes old runs, the proof chain survives pruning, the cadence
// is weekly, and the Proof SLA tracks the timeline (including downtime).
func TestRetentionAndWeeklyCadence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := fixtures.Dump(t)
	st := newStore(t)

	const keep = 4
	drill, src := soakDrill(dir, config.Retention{MaxCount: keep})

	sched, err := scheduler.ParseSchedule(drill.Schedule)
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}

	now := base
	o := New(st, exec.NewLocal("soak"), func() time.Time { return now })

	const weeks = 8
	var fires []time.Time
	for i := range weeks {
		now = sched.Next(now) // next Sunday 04:10 strictly after now
		fires = append(fires, now)
		res, err := o.Run(ctx, drill, src, RunOptions{Trigger: store.TriggerSchedule})
		if err != nil {
			t.Fatalf("week %d Run: %v", i, err)
		}
		if res.Status != store.ResultPass {
			t.Fatalf("week %d = %s, want pass; levels = %+v", i, res.Status, res.Levels)
		}
	}

	// Cadence: every fire is exactly a week after the previous one.
	for i := 1; i < len(fires); i++ {
		if gap := fires[i].Sub(fires[i-1]); gap != 7*24*time.Hour {
			t.Errorf("fire %d→%d gap = %v, want 168h", i-1, i, gap)
		}
	}

	// Retention by count: only the newest `keep` runs survive, all finalized pass.
	runs, err := st.ListRuns(ctx, drill.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != keep {
		t.Fatalf("stored runs = %d, want %d", len(runs), keep)
	}
	for _, r := range runs {
		if r.Result != store.ResultPass || r.FinishedAt.IsZero() {
			t.Errorf("kept run %d = %s finished=%v, want pass/finished", r.ID, r.Result, !r.FinishedAt.IsZero())
		}
	}
	if !runs[0].StartedAt.Equal(fires[weeks-1]) {
		t.Errorf("newest run started %v, want last fire %v", runs[0].StartedAt, fires[weeks-1])
	}

	// Proof chain: drill_state is never pruned and holds the latest proof.
	at, ok, err := st.GetProof(ctx, drill.Name, "l1")
	if err != nil || !ok {
		t.Fatalf("GetProof: %v ok=%v (proof must survive pruning)", err, ok)
	}
	if !at.Equal(fires[weeks-1]) {
		t.Errorf("last proven = %v, want %v", at, fires[weeks-1])
	}

	// Proof SLA: fresh just after a proof, stale after downtime past the SLA.
	if scheduler.Stale(drill.MaxProofAge.Duration(), at, at.Add(time.Hour)) {
		t.Error("should be within SLA an hour after a proof")
	}
	if !scheduler.Stale(drill.MaxProofAge.Duration(), at, at.Add(30*24*time.Hour)) {
		t.Error("should be stale 30 days after the last proof (10d SLA)")
	}
}

// Retention by age, exercised through the run path: a run far enough ahead prunes
// the runs that fell outside the age window.
func TestRetentionByAgeWiring(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := fixtures.Dump(t)
	st := newStore(t)

	const daydur = 24 * time.Hour
	drill, src := soakDrill(dir, config.Retention{MaxAge: config.Duration(2 * daydur)})

	now := base
	o := New(st, exec.NewLocal("soak"), func() time.Time { return now })

	// Three daily runs: at day 0/1/2 nothing is yet older than 2 days.
	for i := range 3 {
		now = base.Add(time.Duration(i) * daydur)
		if _, err := o.Run(ctx, drill, src, RunOptions{Trigger: store.TriggerSchedule}); err != nil {
			t.Fatalf("day %d Run: %v", i, err)
		}
	}
	if runs, _ := st.ListRuns(ctx, drill.Name, 0); len(runs) != 3 {
		t.Fatalf("after 3 daily runs = %d, want 3 (all within 2d at prune time)", len(runs))
	}

	// A run at day 5 prunes everything started before day 3 — the first three.
	now = base.Add(5 * daydur)
	if _, err := o.Run(ctx, drill, src, RunOptions{Trigger: store.TriggerSchedule}); err != nil {
		t.Fatalf("day 5 Run: %v", err)
	}
	runs, err := st.ListRuns(ctx, drill.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].StartedAt.Equal(now) {
		t.Fatalf("after age prune = %d runs (newest %v), want 1 at %v", len(runs), runs[0].StartedAt, now)
	}
}
