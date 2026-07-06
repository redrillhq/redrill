// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package report

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/store"
)

var update = flag.Bool("update", false, "update golden files")

const testConfig = `
version: 1
data_dir: /var/lib/redrill
scratch: {dir: /var/lib/redrill/scratch}
sources:
  - {name: dumps, type: dumpdir, path: /backups/db, pattern: "*.sql.gz"}
  - {name: media, type: dumpdir, path: /backups/media, pattern: "*.tar.gz"}
drills:
  - name: app-db
    source: dumps
    schedule: "@daily"
    max_proof_age: 10d
    levels:
      l1: {file_min_bytes: 1KiB, compression_test: true, max_age: 2d}
      l3:
        sandbox: {image: postgres:16}
        checks:
          - sql: {query: "select count(*) from users", expect: "== 5"}
  - name: photos
    source: media
    max_proof_age: 10d
    levels:
      l1: {file_min_bytes: 1KiB}
  - name: wiki
    source: dumps
    schedule: "@weekly"
    max_proof_age: 10d
    levels:
      l1: {file_min_bytes: 1KiB, compression_test: true}
      l3:
        sandbox: {image: postgres:16}
        checks:
          - sql: {query: "select count(*) from pages", expect: "> 0"}
`

var (
	now        = time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC)
	passFinish = time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)  // 3d ago
	oldProof   = time.Date(2026, 6, 14, 9, 0, 0, 0, time.UTC) // 20d ago, past the 10d SLA
)

// seedStore builds the fixture picture: app-db proven and green, photos never
// run, wiki failing with a stale proof.
func seedStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "redrill.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// app-db: a full L1+L3 pass, proven at both levels.
	start := passFinish.Add(-12300 * time.Millisecond)
	id, err := st.CreateRun(ctx, store.Run{Drill: "app-db", Trigger: store.TriggerSchedule, StartedAt: start})
	if err != nil {
		t.Fatal(err)
	}
	mustFinish(t, st, store.Run{
		ID: id, FinishedAt: passFinish, Result: store.ResultPass, LevelReached: "l3",
		BytesRestored: 52428800, FilesRestored: 240, DurationMS: 12300,
		Snapshot: "app-db-2026-07-01.sql.gz",
	})
	mustStep(t, st, store.RunStep{RunID: id, Kind: "l1", StartedAt: start, FinishedAt: start.Add(400 * time.Millisecond), Status: "pass", Summary: "3 checks passed"})
	mustStep(t, st, store.RunStep{RunID: id, Kind: "l3", StartedAt: start.Add(400 * time.Millisecond), FinishedAt: passFinish, Status: "pass", Summary: "sandbox booted, 1 check passed"})
	mustEvidence(t, st,
		store.Evidence{RunID: id, CheckKind: "file_min_bytes", Target: "app-db-2026-07-01.sql.gz", Expected: "> 1024", Actual: "52428800", Status: "pass"},
		store.Evidence{RunID: id, CheckKind: "compression_test", Target: "app-db-2026-07-01.sql.gz", Expected: "valid gzip", Actual: "ok", Status: "pass"},
		store.Evidence{RunID: id, CheckKind: "canary_file", Target: "backup-canary.txt", Expected: "present", Actual: "present", Status: "pass", Weak: true},
		store.Evidence{RunID: id, CheckKind: "sql", Target: "select count(*) from users", Expected: "== 5", Actual: "5", Status: "pass"},
	)
	mustProof(t, st, "app-db", "l1", passFinish)
	mustProof(t, st, "app-db", "l3", passFinish)

	// wiki: an L1 fail short-circuiting L3, and only a stale old proof.
	wStart := now.Add(-2 * time.Hour)
	wid, err := st.CreateRun(ctx, store.Run{Drill: "wiki", Trigger: store.TriggerSchedule, StartedAt: wStart})
	if err != nil {
		t.Fatal(err)
	}
	mustFinish(t, st, store.Run{
		ID: wid, FinishedAt: wStart.Add(700 * time.Millisecond), Result: store.ResultFail, LevelReached: "l1", DurationMS: 700,
		Snapshot: "wiki-2026-07-04.sql.gz",
	})
	mustStep(t, st, store.RunStep{RunID: wid, Kind: "l1", StartedAt: wStart, FinishedAt: wStart.Add(700 * time.Millisecond), Status: "fail", Summary: "file_min_bytes failed"})
	mustStep(t, st, store.RunStep{RunID: wid, Kind: "l3", StartedAt: wStart.Add(700 * time.Millisecond), Status: "skipped", Summary: "skipped: l1 failed"})
	mustEvidence(t, st,
		store.Evidence{RunID: wid, CheckKind: "file_min_bytes", Target: "wiki-2026-07-04.sql.gz", Expected: "> 1024", Actual: "0", Status: "fail"},
		store.Evidence{RunID: wid, CheckKind: "compression_test", Target: "wiki-2026-07-04.sql.gz", Expected: "valid gzip", Actual: "unexpected EOF", Status: "fail"},
	)
	mustProof(t, st, "wiki", "l1", oldProof)

	return st
}

func mustFinish(t *testing.T, st *store.Store, r store.Run) {
	t.Helper()
	if err := st.FinishRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func mustStep(t *testing.T, st *store.Store, s store.RunStep) {
	t.Helper()
	if err := st.AddStep(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func mustEvidence(t *testing.T, st *store.Store, evs ...store.Evidence) {
	t.Helper()
	for _, e := range evs {
		if err := st.AddEvidence(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func mustProof(t *testing.T, st *store.Store, drill, level string, at time.Time) {
	t.Helper()
	if err := st.RecordProof(context.Background(), drill, level, at); err != nil {
		t.Fatal(err)
	}
}

func buildReport(t *testing.T) Report {
	t.Helper()
	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	st := seedStore(t)
	r, err := Build(context.Background(), st, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuild(t *testing.T) {
	t.Parallel()
	r := buildReport(t)

	if r.Total != 3 || r.ProvenOK != 1 {
		t.Errorf("headline: got %d of %d, want 1 of 3", r.ProvenOK, r.Total)
	}
	byName := map[string]*Drill{}
	for i := range r.Drills {
		byName[r.Drills[i].Name] = &r.Drills[i]
	}

	app := byName["app-db"]
	if app.Stale {
		t.Error("app-db: proven 3d ago within a 10d SLA must not be stale")
	}
	if app.SourceType != "dumpdir" || app.HeadlineLevel != "l3" {
		t.Errorf("app-db: source_type=%q headline=%q", app.SourceType, app.HeadlineLevel)
	}
	if app.LastRun == nil || app.LastRun.Result != "pass" || len(app.LastRun.Evidence) != 4 {
		t.Fatalf("app-db last run: %+v", app.LastRun)
	}
	if app.LastRun.Snapshot != "app-db-2026-07-01.sql.gz" {
		t.Errorf("app-db snapshot = %q, want the audited dump", app.LastRun.Snapshot)
	}
	if got := app.LastRun.Steps[0].DurationMS; got != 400 {
		t.Errorf("l1 step duration: got %d ms, want 400", got)
	}
	if !app.LastRun.Evidence[2].Weak {
		t.Error("canary_file evidence must stay weak-labeled")
	}

	photos := byName["photos"]
	if !photos.Stale || photos.LastRun != nil || len(photos.Proofs) != 0 {
		t.Errorf("photos: want never-run stale drill, got %+v", photos)
	}
	if photos.Schedule != "" {
		t.Errorf("photos: manual-only drill has schedule %q", photos.Schedule)
	}

	wiki := byName["wiki"]
	if !wiki.Stale {
		t.Error("wiki: a 20d-old proof against a 10d SLA must be stale")
	}
	if wiki.LastRun == nil || wiki.LastRun.Result != "fail" {
		t.Fatalf("wiki last run: %+v", wiki.LastRun)
	}
	if wiki.LastRun.Steps[1].Status != "skipped" {
		t.Errorf("wiki l3 step: got %q, want skipped", wiki.LastRun.Steps[1].Status)
	}
}

func TestRenderGolden(t *testing.T) {
	t.Parallel()
	r := buildReport(t)

	md := Markdown(r)
	html, err := HTML(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		got  []byte
	}{
		{"report.md", md},
		{"report.html", html},
	} {
		golden := filepath.Join("testdata", tt.name+".golden")
		if *update {
			if err := os.WriteFile(golden, tt.got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if string(tt.got) != string(want) {
			t.Errorf("%s mismatch:\n--- got ---\n%s\n--- want ---\n%s", tt.name, tt.got, want)
		}
	}
}

// The verdict vocabulary must stay distinct in both formats: fail is never
// conflated with error, and skipped stays visible (invariant #3).
func TestVerdictsDistinct(t *testing.T) {
	t.Parallel()
	r := buildReport(t)
	md := string(Markdown(r))
	html, err := HTML(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FAIL", "PASS", "SKIPPED", "(weak)", "STALE", "within SLA"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	for _, want := range []string{"status-pass", "status-fail", "status-skipped", "sla-stale", "sla-ok", "weak"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("html missing %q", want)
		}
	}
}

// A pipe in evidence must not break the Markdown table.
func TestMarkdownEscapes(t *testing.T) {
	t.Parallel()
	r := Report{GeneratedAt: now, Total: 1, Drills: []Drill{{
		Name: "d", SourceName: "s", SourceType: "dumpdir",
		LastRun: &Run{Result: "pass", Trigger: "manual", StartedAt: now, FinishedAt: now,
			Evidence: []Evidence{{CheckKind: "sql", Target: "a|b", Expected: "x\ny", Actual: "ok", Status: "pass"}}},
	}}}
	md := string(Markdown(r))
	if strings.Contains(md, "| a|b |") {
		t.Error("unescaped pipe broke the evidence table")
	}
	if !strings.Contains(md, `a\|b`) || !strings.Contains(md, "x y") {
		t.Errorf("escaping missing:\n%s", md)
	}
}
