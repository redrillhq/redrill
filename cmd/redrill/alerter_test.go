// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/checks"
	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/notify"
	"github.com/alyamovsky/redrill/internal/orchestrate"
	"github.com/alyamovsky/redrill/internal/store"
)

type captureSender struct {
	mu    sync.Mutex
	notes []captured
}

type captured struct{ title, body string }

func (c *captureSender) Send(_ context.Context, title, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notes = append(c.notes, captured{title, body})
	return nil
}

func (c *captureSender) all() []captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]captured(nil), c.notes...)
}

func newTestAlerter(st *store.Store, drills []config.Drill, now time.Time) (*alerter, *captureSender) {
	cs := &captureSender{}
	n := notify.NewWithSender(cs, []string{"fail", "error", "recover", "stale"}, nil)
	return newAlerter(n, st, drills, func() time.Time { return now }), cs
}

func l1Drill(name string, maxProofAge time.Duration) config.Drill {
	return config.Drill{Name: name, Source: "dumps", MaxProofAge: config.Duration(maxProofAge), Levels: config.Levels{L1: &config.L1{}}}
}

func TestAfterRunFail(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	_, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		al, cs := newTestAlerter(st, []config.Drill{l1Drill("app-db", 10*24*time.Hour)}, now)
		res := orchestrate.RunResult{
			RunID: 1, Status: store.ResultFail, LevelReached: "l1",
			Levels: []orchestrate.LevelOutcome{{
				Level: "l1", Status: "fail",
				Evidence: []checks.Evidence{{Kind: "max_age", Target: "app.sql.gz", Expected: "age <= 36h", Actual: "age 720h", Status: checks.Fail}},
			}},
		}
		al.afterRun(context.Background(), l1Drill("app-db", 10*24*time.Hour), res)
		notes := cs.all()
		if len(notes) != 1 {
			t.Fatalf("notes = %d, want 1 fail", len(notes))
		}
		if !strings.Contains(notes[0].title, "FAILED") || !strings.Contains(notes[0].body, "max_age") {
			t.Errorf("fail note = %+v, want FAILED title and max_age diagnosis", notes[0])
		}
	})
}

// A pass after a failing run is a recovery.
func TestAfterRunRecover(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	_, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		recordRun(t, st, "app-db", store.ResultFail, now.Add(-2*time.Hour), "l1") // id 1
		recordRun(t, st, "app-db", store.ResultPass, now.Add(-time.Hour), "l1")   // id 2 (current)
		al, cs := newTestAlerter(st, []config.Drill{l1Drill("app-db", 10*24*time.Hour)}, now)

		res := orchestrate.RunResult{RunID: 2, Status: store.ResultPass, LevelReached: "l1"}
		al.afterRun(context.Background(), l1Drill("app-db", 10*24*time.Hour), res)
		notes := cs.all()
		if len(notes) != 1 || !strings.Contains(notes[0].title, "recovered") {
			t.Fatalf("notes = %+v, want 1 recover", notes)
		}
	})
}

// A pass with no prior failing run is silent.
func TestAfterRunFirstPassSilent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	_, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		recordRun(t, st, "app-db", store.ResultPass, now.Add(-time.Hour), "l1") // id 1 (current)
		al, cs := newTestAlerter(st, []config.Drill{l1Drill("app-db", 10*24*time.Hour)}, now)
		res := orchestrate.RunResult{RunID: 1, Status: store.ResultPass, LevelReached: "l1"}
		al.afterRun(context.Background(), l1Drill("app-db", 10*24*time.Hour), res)
		if n := len(cs.all()); n != 0 {
			t.Fatalf("notes = %d, want 0 (first pass is silent)", n)
		}
	})
}

// A stale episode alerts once, recovers, and the flag clears.
func TestSweepStaleOncePerEpisode(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	_, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		drills := []config.Drill{l1Drill("app-db", 10*24*time.Hour)}
		al, cs := newTestAlerter(st, drills, now)

		if err := st.RecordProof(context.Background(), "app-db", "l1", now.Add(-30*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		al.sweepStale(context.Background())
		al.sweepStale(context.Background()) // dedup: no second alert
		notes := cs.all()
		if len(notes) != 1 || !strings.Contains(notes[0].title, "STALE") {
			t.Fatalf("notes = %+v, want exactly 1 STALE", notes)
		}

		// Fresh proof clears the stale flag; a later staleness re-alerts.
		if err := st.RecordProof(context.Background(), "app-db", "l1", now); err != nil {
			t.Fatal(err)
		}
		al.sweepStale(context.Background())
		if n := len(cs.all()); n != 1 {
			t.Fatalf("notes = %d after recovery sweep, want still 1", n)
		}
	})
}

func TestInactiveAlerterNoop(t *testing.T) {
	t.Parallel()
	al := newAlerter(nil, nil, nil, time.Now)
	if al.active() {
		t.Fatal("alerter with nil notifier should be inactive")
	}
	// Must not panic with a nil notifier / nil store.
	al.afterRun(context.Background(), l1Drill("x", time.Hour), orchestrate.RunResult{Status: store.ResultFail})
	al.sweepStale(context.Background())
}
