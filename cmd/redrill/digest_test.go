// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/notify"
	"github.com/redrillhq/redrill/internal/store"
)

// newDigestAlerter builds an alerter whose notifier has weekly_digest enabled
// and whose clock reads *cur.
func newDigestAlerter(st *store.Store, drills []config.Drill, cur *time.Time) (*alerter, *captureSender) {
	cs := &captureSender{}
	n := notify.NewWithSender(cs, []string{"fail", "stale", "weekly_digest"}, nil)
	return newAlerter(n, st, drills, func() time.Time { return *cur }), cs
}

// The digest fires once when the Sunday slot passes while the daemon runs,
// never twice for one slot, and again for the next week's slot.
func TestWeeklyDigestOncePerSlot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		cur := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC) // Thursday
		drills := []config.Drill{l1Drill("app-db", 10*24*time.Hour)}
		al, cs := newDigestAlerter(st, drills, &cur)

		recordRun(t, st, "app-db", store.ResultPass, cur.Add(-time.Hour), "l1")
		if err := st.RecordProof(ctx, "app-db", "l1", cur.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}

		al.maybeDigest(ctx) // Thursday: before the slot
		if n := len(cs.all()); n != 0 {
			t.Fatalf("notes = %d before the slot, want 0", n)
		}

		cur = time.Date(2026, 7, 5, 8, 30, 0, 0, time.UTC) // Sunday, past 08:00
		al.maybeDigest(ctx)
		al.maybeDigest(ctx) // same slot: no duplicate
		notes := cs.all()
		if len(notes) != 1 {
			t.Fatalf("notes = %d after the slot, want exactly 1", len(notes))
		}
		if !strings.Contains(notes[0].title, "weekly digest") || !strings.Contains(notes[0].title, "1 of 1 proven within SLA") {
			t.Errorf("digest title = %q", notes[0].title)
		}
		if !strings.Contains(notes[0].body, "app-db: proven 2d ago (L1) · ok") {
			t.Errorf("digest body = %q", notes[0].body)
		}

		cur = time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC) // next Sunday
		al.maybeDigest(ctx)
		if n := len(cs.all()); n != 2 {
			t.Fatalf("notes = %d after the second slot, want 2", n)
		}
	})
}

// A daemon started after the slot does not send a catch-up digest — a restart
// can never duplicate one.
func TestWeeklyDigestNoStartupCatchUp(t *testing.T) {
	t.Parallel()
	_, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		cur := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) // Monday, slot passed Sunday
		al, cs := newDigestAlerter(st, []config.Drill{l1Drill("app-db", 10*24*time.Hour)}, &cur)
		al.maybeDigest(context.Background())
		if n := len(cs.all()); n != 0 {
			t.Fatalf("notes = %d, want 0 (no catch-up for a pre-start slot)", n)
		}
	})
}

// Without weekly_digest in events, the slot passing sends nothing.
func TestWeeklyDigestOptIn(t *testing.T) {
	t.Parallel()
	_, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		cur := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
		cs := &captureSender{}
		n := notify.NewWithSender(cs, []string{"fail", "error", "recover", "stale"}, nil)
		al := newAlerter(n, st, []config.Drill{l1Drill("app-db", 10*24*time.Hour)}, func() time.Time { return cur })

		cur = time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
		al.maybeDigest(context.Background())
		if n := len(cs.all()); n != 0 {
			t.Fatalf("notes = %d, want 0 (digest is opt-in)", n)
		}
	})
}
