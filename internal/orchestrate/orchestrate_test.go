package orchestrate

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alyamovsky/drillbit/internal/config"
	"github.com/alyamovsky/drillbit/internal/exec"
	"github.com/alyamovsky/drillbit/internal/store"
)

var base = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func makeGz(t *testing.T, dir, name, body string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "drillbit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func l1Full() config.Levels {
	fmb := config.Size(1)
	ct := true
	ma := config.Duration(36 * time.Hour)
	return config.Levels{L1: &config.L1{FileMinBytes: &fmb, CompressionTest: &ct, MaxAge: &ma}}
}

func drillFor(dir string, levels config.Levels) (config.Drill, config.Source) {
	src := config.Source{Name: "dumps", Type: "dumpdir", Path: dir, Pattern: "*.sql.gz", Pick: "newest"}
	return config.Drill{Name: "app-db", Source: "dumps", Levels: levels}, src
}

func runDrill(t *testing.T, st *store.Store, drill config.Drill, src config.Source, opts RunOptions) RunResult {
	t.Helper()
	o := New(st, exec.NewLocal("testhost"), func() time.Time { return base })
	res, err := o.Run(context.Background(), drill, src, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRunPassWritesEvidenceAndProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-1*time.Hour))
	st := newStore(t)
	drill, src := drillFor(dir, l1Full())

	res := runDrill(t, st, drill, src, RunOptions{})
	if res.Status != store.ResultPass || res.LevelReached != "l1" {
		t.Fatalf("result = %s level = %s, want pass/l1", res.Status, res.LevelReached)
	}

	evs, err := st.ListEvidence(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("evidence rows = %d, want 3 (file_min_bytes, compression_test, max_age)", len(evs))
	}
	for _, ev := range evs {
		if ev.Status != "pass" {
			t.Errorf("%s status = %s, want pass", ev.CheckKind, ev.Status)
		}
	}

	at, ok, err := st.GetProof(ctx, "app-db", "l1")
	if err != nil || !ok {
		t.Fatalf("GetProof: %v ok=%v", err, ok)
	}
	if !at.Equal(base) {
		t.Errorf("proof time = %v, want %v", at, base)
	}

	row, err := st.GetRun(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Result != store.ResultPass || row.LevelReached != "l1" || row.FinishedAt.IsZero() {
		t.Errorf("run row = %+v, want finished pass/l1", row)
	}
}

func TestRunFailOnStaleNoProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-30*24*time.Hour)) // stale
	st := newStore(t)
	drill, src := drillFor(dir, l1Full())

	res := runDrill(t, st, drill, src, RunOptions{})
	if res.Status != store.ResultFail {
		t.Fatalf("result = %s, want fail", res.Status)
	}
	if _, ok, _ := st.GetProof(ctx, "app-db", "l1"); ok {
		t.Error("a failed run must not record a proof")
	}
}

// fail (backup bad) and error (auditor blind) must stay distinct.
func TestRunErrorOnUnreadableDir(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore(t)
	drill, src := drillFor(filepath.Join(t.TempDir(), "nope"), l1Full())

	res := runDrill(t, st, drill, src, RunOptions{})
	if res.Status != store.ResultError {
		t.Fatalf("result = %s, want error (distinct from fail)", res.Status)
	}
	if _, ok, _ := st.GetProof(ctx, "app-db", "l1"); ok {
		t.Error("an errored run must not record a proof")
	}
}

func TestRunShortCircuitsHigherLevels(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-30*24*time.Hour)) // L1 will fail (stale)
	levels := l1Full()
	levels.L3 = &config.L3{} // configured but unimplemented in M5

	st := newStore(t)
	drill, src := drillFor(dir, levels)
	res := runDrill(t, st, drill, src, RunOptions{})

	if res.Status != store.ResultFail {
		t.Fatalf("result = %s, want fail", res.Status)
	}
	var l3 LevelOutcome
	for _, lv := range res.Levels {
		if lv.Level == "l3" {
			l3 = lv
		}
	}
	if l3.Status != statusSkipped {
		t.Fatalf("l3 status = %s, want skipped", l3.Status)
	}
	if !strings.Contains(l3.Summary, "lower level") {
		t.Errorf("l3 skip summary = %q, want it to cite the short-circuit", l3.Summary)
	}
}

func TestRunUnimplementedLevelSkippedNotFailed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-1*time.Hour)) // L1 passes
	levels := l1Full()
	levels.L3 = &config.L3{}

	st := newStore(t)
	drill, src := drillFor(dir, levels)
	res := runDrill(t, st, drill, src, RunOptions{})

	// L1 passed; L3 is unimplemented → skipped, not a failure → run still passes.
	if res.Status != store.ResultPass {
		t.Fatalf("result = %s, want pass (unimplemented L3 must not fail the run)", res.Status)
	}
	var l3 LevelOutcome
	for _, lv := range res.Levels {
		if lv.Level == "l3" {
			l3 = lv
		}
	}
	if l3.Status != statusSkipped || !strings.Contains(l3.Summary, "not implemented") {
		t.Errorf("l3 outcome = %+v, want skipped/not-implemented", l3)
	}
}

func TestRunLevelFilterUnknownLevel(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	drill, src := drillFor(t.TempDir(), l1Full())
	o := New(st, exec.NewLocal("h"), func() time.Time { return base })
	if _, err := o.Run(context.Background(), drill, src, RunOptions{Level: "l3"}); err == nil {
		t.Fatal("asking for an unconfigured level should error")
	}
}

func TestRunReportStreamsPerLevel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-1*time.Hour))
	st := newStore(t)
	drill, src := drillFor(dir, l1Full())

	var seen []string
	runDrill(t, st, drill, src, RunOptions{Report: func(o LevelOutcome) { seen = append(seen, o.Level) }})
	if len(seen) != 1 || seen[0] != "l1" {
		t.Errorf("streamed levels = %v, want [l1]", seen)
	}
}
