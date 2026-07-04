// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDigest() Digest {
	now := time.Date(2026, 7, 5, 8, 0, 0, 0, time.UTC)
	return Digest{
		Now: now,
		Entries: []DigestEntry{
			{Drill: "app-db", Level: "l3", LastProven: now.Add(-3 * 24 * time.Hour), MaxProofAge: 10 * 24 * time.Hour, LastResult: "pass", Bytes: 52428800},
			{Drill: "photos", Level: "l1", Stale: true, MaxProofAge: 10 * 24 * time.Hour},
			{Drill: "wiki", Level: "l3", LastProven: now.Add(-20 * 24 * time.Hour), Stale: true, MaxProofAge: 10 * 24 * time.Hour, LastResult: "fail"},
			{Drill: "scratchpad", Level: "l1", LastProven: now.Add(-time.Hour), LastResult: "pass"},
		},
	}
}

func TestRenderDigestGolden(t *testing.T) {
	t.Parallel()
	title, body := RenderDigest(testDigest())
	got := title + "\n\n" + body
	golden := filepath.Join("testdata", "digest.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("digest mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderDigestContent(t *testing.T) {
	t.Parallel()
	title, body := RenderDigest(testDigest())
	if !strings.Contains(title, "2 of 4 proven within SLA") {
		t.Errorf("title = %q, want the 2-of-4 headline", title)
	}
	for _, want := range []string{
		"app-db: proven 3d ago (L3) · ok · 50 MiB verified",
		"photos: never proven · STALE (SLA 10d)",
		"wiki: proven 20d ago (L3) · STALE (SLA 10d) · last run: fail",
		"scratchpad: proven 1h ago (L1) · no SLA",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

// DispatchDigest respects the enabled-events filter, and a nil notifier is a
// safe no-op.
func TestDispatchDigestGating(t *testing.T) {
	t.Parallel()
	var sent int
	s := senderFunc(func(context.Context, string, string) error { sent++; return nil })

	off := NewWithSender(s, []string{"fail", "error"}, nil)
	off.DispatchDigest(context.Background(), testDigest())
	if sent != 0 {
		t.Fatal("digest sent though weekly_digest is not enabled")
	}
	if off.DigestEnabled() {
		t.Error("DigestEnabled true without weekly_digest")
	}

	on := NewWithSender(s, []string{"weekly_digest"}, nil)
	if !on.DigestEnabled() {
		t.Error("DigestEnabled false with weekly_digest")
	}
	on.DispatchDigest(context.Background(), testDigest())
	if sent != 1 {
		t.Fatalf("sent = %d, want 1", sent)
	}

	var nilN *Notifier
	nilN.DispatchDigest(context.Background(), testDigest()) // must not panic
}

type senderFunc func(context.Context, string, string) error

func (f senderFunc) Send(ctx context.Context, title, body string) error { return f(ctx, title, body) }
