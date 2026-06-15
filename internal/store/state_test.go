// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"
)

func TestRecordProofPerLevel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	t1 := epoch
	t2 := epoch.Add(time.Hour)
	if err := s.RecordProof(ctx, "d", "l1", t1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProof(ctx, "d", "l2", t2); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		level string
		want  time.Time
	}{{"l1", t1}, {"l2", t2}} {
		got, ok, err := s.GetProof(ctx, "d", tc.level)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%s proof = (%v, %v), want %v", tc.level, got, ok, tc.want)
		}
	}
}

func TestRecordProofAdvances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if err := s.RecordProof(ctx, "d", "l1", epoch); err != nil {
		t.Fatal(err)
	}
	later := epoch.Add(7 * 24 * time.Hour)
	if err := s.RecordProof(ctx, "d", "l1", later); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetProof(ctx, "d", "l1")
	if err != nil || !ok {
		t.Fatalf("GetProof: %v ok=%v", err, ok)
	}
	if !got.Equal(later) {
		t.Errorf("proof = %v, want advanced to %v", got, later)
	}
	if got.Location() != time.UTC {
		t.Errorf("proof location = %v, want UTC", got.Location())
	}
}

// recordOnPass models the orchestrator policy: drill_state advances only on a
// pass.
func recordOnPass(ctx context.Context, t *testing.T, s *Store, drill string, at time.Time, results map[string]Result) {
	t.Helper()
	for level, res := range results {
		if res != ResultPass {
			continue
		}
		if err := s.RecordProof(ctx, drill, level, at); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDrillStateOnlyOnPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	// L1 passes, L2 fails → only L1 proven.
	recordOnPass(ctx, t, s, "d", epoch, map[string]Result{"l1": ResultPass, "l2": ResultFail})
	if _, ok, _ := s.GetProof(ctx, "d", "l1"); !ok {
		t.Error("L1 should be proven after a pass")
	}
	if _, ok, _ := s.GetProof(ctx, "d", "l2"); ok {
		t.Error("L2 must not be proven after a fail")
	}

	// A week later both pass → L1 advances, L2 now proven.
	week := epoch.Add(7 * 24 * time.Hour)
	recordOnPass(ctx, t, s, "d", week, map[string]Result{"l1": ResultPass, "l2": ResultPass})

	l1, ok, _ := s.GetProof(ctx, "d", "l1")
	if !ok || !l1.Equal(week) {
		t.Errorf("L1 proof = (%v, %v), want %v", l1, ok, week)
	}
	l2, ok, _ := s.GetProof(ctx, "d", "l2")
	if !ok || !l2.Equal(week) {
		t.Errorf("L2 proof = (%v, %v), want %v", l2, ok, week)
	}

	// L2 errors (not a backup failure) → no advance.
	twoWeeks := epoch.Add(14 * 24 * time.Hour)
	recordOnPass(ctx, t, s, "d", twoWeeks, map[string]Result{"l2": ResultError})
	l2, _, _ = s.GetProof(ctx, "d", "l2")
	if !l2.Equal(week) {
		t.Errorf("L2 proof advanced on error: %v, want held at %v", l2, week)
	}
}

func TestGetProofNotFound(t *testing.T) {
	t.Parallel()
	got, ok, err := newStore(t).GetProof(context.Background(), "d", "l1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || !got.IsZero() {
		t.Errorf("got (%v, %v), want (zero, false)", got, ok)
	}
}

func TestListProofs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if err := s.RecordProof(ctx, "d", "l3", epoch); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProof(ctx, "d", "l1", epoch); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProof(ctx, "other", "l1", epoch); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListProofs(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Level != "l1" || got[1].Level != "l3" {
		t.Errorf("ListProofs = %+v, want [l1 l3] for drill d only", got)
	}
}

func TestRecordProofValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	tests := []struct {
		name         string
		drill, level string
		at           time.Time
	}{
		{"empty drill", "", "l1", epoch},
		{"empty level", "d", "", epoch},
		{"zero time", "d", "l1", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := s.RecordProof(ctx, tt.drill, tt.level, tt.at); err == nil {
				t.Fatal("want error")
			}
		})
	}
}
