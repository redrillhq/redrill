package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

const day = 24 * time.Hour

func mustRun(ctx context.Context, t *testing.T, s *Store, drill string, at time.Time) int64 {
	t.Helper()
	id, err := s.CreateRun(ctx, Run{Drill: drill, Trigger: TriggerSchedule, StartedAt: at})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return id
}

func runIDs(ctx context.Context, t *testing.T, s *Store, drill string) []int64 {
	t.Helper()
	runs, err := s.ListRuns(ctx, drill, 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	ids := make([]int64, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	return ids
}

func TestPruneByCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	var ids []int64
	for i := range 5 {
		ids = append(ids, mustRun(ctx, t, s, "d", epoch.Add(time.Duration(i)*time.Hour)))
	}
	keepOther := mustRun(ctx, t, s, "other", epoch) // must survive pruning "d"

	n, err := s.Prune(ctx, "d", 0, 2, epoch)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("deleted = %d, want 3", n)
	}
	if got := runIDs(ctx, t, s, "d"); len(got) != 2 || got[0] != ids[4] || got[1] != ids[3] {
		t.Errorf("kept = %v, want newest two %v,%v", got, ids[4], ids[3])
	}
	if got := runIDs(ctx, t, s, "other"); len(got) != 1 || got[0] != keepOther {
		t.Errorf("pruning d affected other: %v", got)
	}
}

func TestPruneByAge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	mustRun(ctx, t, s, "d", epoch.Add(0*day))
	mustRun(ctx, t, s, "d", epoch.Add(2*day))
	keep1 := mustRun(ctx, t, s, "d", epoch.Add(5*day))
	keep2 := mustRun(ctx, t, s, "d", epoch.Add(9*day))

	now := epoch.Add(10 * day)
	// maxAge 7d → cutoff epoch+3d; earlier runs pruned.
	n, err := s.Prune(ctx, "d", 7*day, 0, now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}
	if got := runIDs(ctx, t, s, "d"); len(got) != 2 || got[0] != keep2 || got[1] != keep1 {
		t.Errorf("kept = %v, want %v,%v (within 7d)", got, keep2, keep1)
	}
}

func TestPruneByAgeAndCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	var ids []int64
	for i := range 6 { // started at day 0..5
		ids = append(ids, mustRun(ctx, t, s, "d", epoch.Add(time.Duration(i)*day)))
	}
	now := epoch.Add(5 * day)
	// age cutoff day3 drops 0,1,2; count keeps 4,5; union drops 0,1,2,3.
	n, err := s.Prune(ctx, "d", 2*day, 2, now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 4 {
		t.Errorf("deleted = %d, want 4", n)
	}
	if got := runIDs(ctx, t, s, "d"); len(got) != 2 || got[0] != ids[5] || got[1] != ids[4] {
		t.Errorf("kept = %v, want %v,%v", got, ids[5], ids[4])
	}
}

func TestPruneCascadesAndKeepsProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	old := mustRun(ctx, t, s, "d", epoch)
	if err := s.AddStep(ctx, RunStep{RunID: old, Kind: "plan", StartedAt: epoch, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEvidence(ctx, Evidence{RunID: old, CheckKind: "sql", Status: "pass"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddArtifact(ctx, Artifact{RunID: old, Name: "log", Path: "/p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProof(ctx, "d", "l1", epoch); err != nil {
		t.Fatal(err)
	}
	mustRun(ctx, t, s, "d", epoch.Add(time.Hour)) // newer, kept

	if _, err := s.Prune(ctx, "d", 0, 1, epoch); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if _, err := s.GetRun(ctx, old); !errors.Is(err, ErrNotFound) {
		t.Errorf("old run still present: %v", err)
	}
	if steps, _ := s.ListSteps(ctx, old); len(steps) != 0 {
		t.Errorf("steps not cascaded: %d remain", len(steps))
	}
	if ev, _ := s.ListEvidence(ctx, old); len(ev) != 0 {
		t.Errorf("evidence not cascaded: %d remain", len(ev))
	}
	if arts, _ := s.ListArtifacts(ctx, old); len(arts) != 0 {
		t.Errorf("artifacts not cascaded: %d remain", len(arts))
	}
	// drill_state is never pruned.
	if _, ok, _ := s.GetProof(ctx, "d", "l1"); !ok {
		t.Error("proof was pruned; drill_state must be kept forever")
	}
}

func TestPruneNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	mustRun(ctx, t, s, "d", epoch)

	n, err := s.Prune(ctx, "d", 0, 0, epoch)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 0 {
		t.Errorf("deleted = %d, want 0 (both caps disabled)", n)
	}
	if got := runIDs(ctx, t, s, "d"); len(got) != 1 {
		t.Errorf("runs = %d, want 1 kept", len(got))
	}
}

func TestPruneAgeRequiresNow(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if _, err := s.Prune(context.Background(), "d", 7*day, 0, time.Time{}); err == nil {
		t.Fatal("want error: age-based prune needs a now")
	}
}
