// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	id, err := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerSchedule, StartedAt: epoch, Executor: "local"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateRun returned id 0")
	}

	got, err := s.GetRun(ctx, id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Result != "" || !got.FinishedAt.IsZero() {
		t.Errorf("fresh run should be unfinished: result=%q finished=%v", got.Result, got.FinishedAt)
	}
	if got.Trigger != TriggerSchedule || got.Executor != "local" || !got.StartedAt.Equal(epoch) {
		t.Errorf("run fields mismatch: %+v", got)
	}

	fin := epoch.Add(90 * time.Second)
	snapAt := epoch.Add(-26 * time.Hour)
	err = s.FinishRun(ctx, Run{ID: id, Result: ResultPass, LevelReached: "l2", BytesRestored: 4096, DurationMS: 90000, FinishedAt: fin, Snapshot: "arch-7", SnapshotTime: snapAt})
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	got, err = s.GetRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result != ResultPass || got.LevelReached != "l2" || got.BytesRestored != 4096 || got.DurationMS != 90000 {
		t.Errorf("finished run mismatch: %+v", got)
	}
	if got.Snapshot != "arch-7" {
		t.Errorf("snapshot = %q, want arch-7 (the audited snapshot must persist)", got.Snapshot)
	}
	if !got.SnapshotTime.Equal(snapAt) {
		t.Errorf("snapshot_time = %v, want %v (the RPO input must persist)", got.SnapshotTime, snapAt)
	}
	if !got.FinishedAt.Equal(fin) || got.FinishedAt.Location() != time.UTC {
		t.Errorf("finished_at = %v, want %v UTC", got.FinishedAt, fin)
	}
	if got.Executor != "local" || got.Trigger != TriggerSchedule {
		t.Errorf("FinishRun clobbered identity fields: %+v", got)
	}
}

func TestCreateRunValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	tests := []struct {
		name string
		run  Run
	}{
		{"no drill", Run{Trigger: TriggerManual, StartedAt: epoch}},
		{"no trigger", Run{Drill: "d", StartedAt: epoch}},
		{"no started_at", Run{Drill: "d", Trigger: TriggerManual}},
		{"bad trigger", Run{Drill: "d", Trigger: Trigger("cron"), StartedAt: epoch}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := s.CreateRun(ctx, tt.run); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

// The runs.result CHECK rejects a bad verdict at the storage boundary.
func TestFinishRunRejectsBadResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	id, err := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerManual, StartedAt: epoch})
	if err != nil {
		t.Fatal(err)
	}
	err = s.FinishRun(ctx, Run{ID: id, Result: Result("ok"), FinishedAt: epoch.Add(time.Second)})
	if err == nil {
		t.Fatal("want error for invalid result")
	}
}

func TestFinishRunNotFound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	err := s.FinishRun(context.Background(), Run{ID: 999, Result: ResultPass, FinishedAt: epoch})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetRunNotFound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	_, err := s.GetRun(context.Background(), 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListRunsNewestFirstAndLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	var ids []int64
	for i := range 5 {
		id, err := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerSchedule, StartedAt: epoch.Add(time.Duration(i) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := s.CreateRun(ctx, Run{Drill: "other", Trigger: TriggerManual, StartedAt: epoch}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListRuns(ctx, "d", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("len = %d, want 5", len(all))
	}
	if all[0].ID != ids[4] || all[4].ID != ids[0] {
		t.Errorf("not newest-first: got %d..%d", all[0].ID, all[4].ID)
	}

	limited, err := s.ListRuns(ctx, "d", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 || limited[0].ID != ids[4] || limited[1].ID != ids[3] {
		t.Errorf("limit=2 mismatch: %+v", limited)
	}
}

func TestFilesRestoredAndLastBaselineRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	id1, err := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerManual, StartedAt: epoch})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, Run{ID: id1, Result: ResultPass, FilesRestored: 120, FinishedAt: epoch.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	id2, _ := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerManual, StartedAt: epoch.Add(time.Hour)})
	if err := s.FinishRun(ctx, Run{ID: id2, Result: ResultFail, FilesRestored: 3, FinishedAt: epoch.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// A restore-less pass (e.g. an L1-only --level run) must not become the
	// baseline and reset it to zero.
	id3, _ := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerManual, StartedAt: epoch.Add(2 * time.Hour)})
	if err := s.FinishRun(ctx, Run{ID: id3, Result: ResultPass, FilesRestored: 0, FinishedAt: epoch.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if got, _ := s.GetRun(ctx, id1); got.FilesRestored != 120 {
		t.Errorf("files_restored = %d, want 120", got.FilesRestored)
	}

	// Most recent pass that restored files: id1, not the fail, not the 0-file pass.
	last, ok, err := s.LastBaselineRun(ctx, "d")
	if err != nil || !ok {
		t.Fatalf("LastBaselineRun: %v ok=%v", err, ok)
	}
	if last.ID != id1 || last.FilesRestored != 120 {
		t.Errorf("baseline run = id %d / %d files, want %d / 120", last.ID, last.FilesRestored, id1)
	}
	if _, ok, _ := s.LastBaselineRun(ctx, "ghost"); ok {
		t.Error("ghost drill should have no baseline run")
	}
}

// A recorded verdict is immutable: FinishRun writes exactly once.
func TestFinishRunWriteOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	id, err := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerManual, StartedAt: epoch})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, Run{ID: id, Result: ResultPass, FinishedAt: epoch.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, Run{ID: id, Result: ResultFail, FinishedAt: epoch.Add(time.Hour)}); err == nil {
		t.Fatal("second FinishRun succeeded, want already-finished error")
	}
	got, err := s.GetRun(ctx, id)
	if err != nil || got.Result != ResultPass {
		t.Errorf("run result = %s err=%v, want pass held", got.Result, err)
	}
}

// Runs a crashed process left without a verdict are finalized as error at
// daemon startup; finished runs are untouched.
func TestMarkAbandonedRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	done, _ := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerSchedule, StartedAt: epoch})
	if err := s.FinishRun(ctx, Run{ID: done, Result: ResultPass, FinishedAt: epoch.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	zombie, _ := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerSchedule, StartedAt: epoch.Add(time.Hour)})

	now := epoch.Add(2 * time.Hour)
	n, err := s.MarkAbandonedRuns(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("MarkAbandonedRuns = %d, %v; want 1, nil", n, err)
	}
	z, err := s.GetRun(ctx, zombie)
	if err != nil || z.Result != ResultError || !z.FinishedAt.Equal(now) {
		t.Errorf("zombie = %s finished %v err=%v, want error at %v", z.Result, z.FinishedAt, err, now)
	}
	d, _ := s.GetRun(ctx, done)
	if d.Result != ResultPass {
		t.Errorf("finished run result = %s, want pass untouched", d.Result)
	}
}

func TestLatestFinishedRunAndBytesCounter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	// No runs yet.
	if _, ok, err := s.LatestFinishedRun(ctx, "d"); err != nil || ok {
		t.Fatalf("LatestFinishedRun on empty: ok=%v err=%v", ok, err)
	}
	if total, err := s.BytesRestoredTotal(ctx, "d"); err != nil || total != 0 {
		t.Fatalf("BytesRestoredTotal on empty: %d err=%v", total, err)
	}

	id1, _ := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerSchedule, StartedAt: epoch})
	if err := s.FinishRun(ctx, Run{ID: id1, Result: ResultPass, LevelReached: "l1", BytesRestored: 1000, FinishedAt: epoch.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	id2, _ := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerSchedule, StartedAt: epoch.Add(time.Hour)})
	if err := s.FinishRun(ctx, Run{ID: id2, Result: ResultFail, LevelReached: "l2", BytesRestored: 500, FinishedAt: epoch.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// A newer, still-running run must not count as the latest *finished* run.
	if _, err := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerManual, StartedAt: epoch.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	last, ok, err := s.LatestFinishedRun(ctx, "d")
	if err != nil || !ok {
		t.Fatalf("LatestFinishedRun: ok=%v err=%v", ok, err)
	}
	if last.ID != id2 || last.Result != ResultFail {
		t.Errorf("latest finished = id %d (%s), want id %d (fail)", last.ID, last.Result, id2)
	}

	// The counter accumulates and survives run pruning (it lives outside runs).
	if err := s.AddBytesRestored(ctx, "d", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBytesRestored(ctx, "d", 500); err != nil {
		t.Fatal(err)
	}
	if total, err := s.BytesRestoredTotal(ctx, "d"); err != nil || total != 1500 {
		t.Errorf("BytesRestoredTotal = %d err=%v, want 1500", total, err)
	}
	if _, err := s.Prune(ctx, "d", 0, 1, epoch.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if total, err := s.BytesRestoredTotal(ctx, "d"); err != nil || total != 1500 {
		t.Errorf("BytesRestoredTotal after prune = %d err=%v, want 1500 (counters are never pruned)", total, err)
	}
	if total, _ := s.BytesRestoredTotal(ctx, "ghost"); total != 0 {
		t.Errorf("BytesRestoredTotal ghost = %d, want 0", total)
	}
}

func TestStepsEvidenceArtifacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	id, err := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerManual, StartedAt: epoch})
	if err != nil {
		t.Fatal(err)
	}

	for i, kind := range []string{"plan", "restore", "checks"} {
		st := RunStep{RunID: id, Kind: kind, StartedAt: epoch.Add(time.Duration(i) * time.Minute), Status: "ok", Summary: kind + " done"}
		if err := s.AddStep(ctx, st); err != nil {
			t.Fatalf("AddStep: %v", err)
		}
	}
	steps, err := s.ListSteps(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(steps))
	}
	for i, st := range steps {
		if st.Idx != i {
			t.Errorf("step[%d].Idx = %d, want %d (auto-assigned, ordered)", i, st.Idx, i)
		}
	}
	if steps[1].Kind != "restore" {
		t.Errorf("steps not in insertion order: %+v", steps)
	}

	weakEv := Evidence{RunID: id, CheckKind: "canary_file", Target: "CANARY", Expected: "present", Actual: "present", Status: "pass", Weak: true}
	strongEv := Evidence{RunID: id, CheckKind: "sql", Target: "users", Expected: "> 0", Actual: "42", Status: "pass"}
	for _, ev := range []Evidence{strongEv, weakEv} {
		if err := s.AddEvidence(ctx, ev); err != nil {
			t.Fatalf("AddEvidence: %v", err)
		}
	}
	evs, err := s.ListEvidence(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("evidence = %d, want 2", len(evs))
	}
	if evs[0].Weak || !evs[1].Weak {
		t.Errorf("weak flag round-trip wrong: %+v", evs)
	}

	if err := s.AddArtifact(ctx, Artifact{RunID: id, Name: "run.log", Path: "/a/run.log", Bytes: 1234}); err != nil {
		t.Fatalf("AddArtifact: %v", err)
	}
	arts, err := s.ListArtifacts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Name != "run.log" || arts[0].Bytes != 1234 {
		t.Errorf("artifacts mismatch: %+v", arts)
	}
}

// Proves foreign_keys is ON: a step for an unknown run is rejected.
func TestAddStepUnknownRunFails(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	err := s.AddStep(context.Background(), RunStep{RunID: 404, Kind: "plan", StartedAt: epoch, Status: "ok"})
	if err == nil {
		t.Fatal("want foreign-key error for unknown run")
	}
}

func TestAddStepRequiresStartedAt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	id, err := s.CreateRun(ctx, Run{Drill: "d", Trigger: TriggerManual, StartedAt: epoch})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddStep(ctx, RunStep{RunID: id, Kind: "plan", Status: "ok"}); err == nil {
		t.Fatal("want error for zero started_at")
	}
}
