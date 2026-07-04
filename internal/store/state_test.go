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

// The pass-only proof policy lives in the orchestrator and is tested there
// (TestProofOnlyOnFullPass); the store's own guarantee is that a proof never
// regresses when runs finish out of order (daemon + CLI overlap).
func TestRecordProofNeverRegresses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	later := epoch.Add(7 * 24 * time.Hour)
	if err := s.RecordProof(ctx, "d", "l1", later); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProof(ctx, "d", "l1", epoch); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetProof(ctx, "d", "l1")
	if err != nil || !ok {
		t.Fatalf("GetProof: %v ok=%v", err, ok)
	}
	if !got.Equal(later) {
		t.Errorf("proof = %v, want held at %v (an older run must not regress it)", got, later)
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

// ProofDrills is the distinct, sorted set of proof-holding drills.
func TestProofDrills(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if got, err := s.ProofDrills(ctx); err != nil || got != nil {
		t.Fatalf("empty store: got %v, %v", got, err)
	}
	for _, p := range []struct{ drill, level string }{
		{"zeta", "l1"}, {"alpha", "l1"}, {"alpha", "l2"},
	} {
		if err := s.RecordProof(ctx, p.drill, p.level, epoch); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ProofDrills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("got %v, want [alpha zeta] (distinct, sorted)", got)
	}
}
