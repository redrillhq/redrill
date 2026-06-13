package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSourceRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	src := Source{Name: "borg1", Type: "borg", ConfigHash: "h1", CreatedAt: epoch}
	if err := s.UpsertSource(ctx, src); err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}

	got, err := s.GetSource(ctx, "borg1")
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if got.Type != "borg" || got.ConfigHash != "h1" {
		t.Errorf("got %+v, want type=borg hash=h1", got)
	}
	if !got.CreatedAt.Equal(epoch) || got.CreatedAt.Location() != time.UTC {
		t.Errorf("created_at = %v (loc %v), want %v UTC", got.CreatedAt, got.CreatedAt.Location(), epoch)
	}
}

func TestUpsertSourcePreservesCreatedAt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if err := s.UpsertSource(ctx, Source{Name: "s", Type: "borg", ConfigHash: "h1", CreatedAt: epoch}); err != nil {
		t.Fatal(err)
	}
	// Re-upsert with a later created_at and changed fields: type/hash update,
	// created_at stays at the original.
	later := epoch.Add(24 * time.Hour)
	if err := s.UpsertSource(ctx, Source{Name: "s", Type: "restic", ConfigHash: "h2", CreatedAt: later}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSource(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "restic" || got.ConfigHash != "h2" {
		t.Errorf("update not applied: %+v", got)
	}
	if !got.CreatedAt.Equal(epoch) {
		t.Errorf("created_at = %v, want preserved %v", got.CreatedAt, epoch)
	}
}

func TestListSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	for _, n := range []string{"c", "a", "b"} {
		if err := s.UpsertSource(ctx, Source{Name: n, Type: "borg", CreatedAt: epoch}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("[%d] = %q, want %q (sorted by name)", i, got[i].Name, w)
		}
	}
}

func TestGetSourceNotFound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	_, err := s.GetSource(context.Background(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpsertSourceValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	tests := []struct {
		name string
		src  Source
	}{
		{"empty name", Source{Type: "borg", CreatedAt: epoch}},
		{"zero created_at", Source{Name: "s", Type: "borg"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := s.UpsertSource(ctx, tt.src); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestDrillRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	d := Drill{Name: "d1", Source: "borg1", ConfigHash: "h", MaxProofAge: 10 * 24 * time.Hour, LevelsJSON: `{"l1":{}}`}
	if err := s.UpsertDrill(ctx, d); err != nil {
		t.Fatalf("UpsertDrill: %v", err)
	}
	got, err := s.GetDrill(ctx, "d1")
	if err != nil {
		t.Fatalf("GetDrill: %v", err)
	}
	if got.Source != "borg1" || got.LevelsJSON != `{"l1":{}}` {
		t.Errorf("got %+v", got)
	}
	if got.MaxProofAge != 10*24*time.Hour {
		t.Errorf("max_proof_age = %v, want 240h", got.MaxProofAge)
	}
}

func TestUpsertDrillUpdates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	if err := s.UpsertDrill(ctx, Drill{Name: "d", Source: "s1", LevelsJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDrill(ctx, Drill{Name: "d", Source: "s2", MaxProofAge: time.Hour, LevelsJSON: `{"l3":{}}`}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDrill(ctx, "d")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "s2" || got.MaxProofAge != time.Hour || got.LevelsJSON != `{"l3":{}}` {
		t.Errorf("update not applied: %+v", got)
	}
}

func TestListDrills(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	for _, n := range []string{"z", "m", "a"} {
		if err := s.UpsertDrill(ctx, Drill{Name: n, Source: "s", MaxProofAge: time.Hour, LevelsJSON: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListDrills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "m", "z"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("[%d] = %q, want %q (sorted by name)", i, got[i].Name, w)
		}
		if got[i].MaxProofAge != time.Hour {
			t.Errorf("[%d] max_proof_age = %v, want 1h", i, got[i].MaxProofAge)
		}
	}
}

func TestGetDrillNotFound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	_, err := s.GetDrill(context.Background(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpsertDrillEmptyName(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if err := s.UpsertDrill(context.Background(), Drill{Source: "s"}); err == nil {
		t.Fatal("want error for empty name")
	}
}
