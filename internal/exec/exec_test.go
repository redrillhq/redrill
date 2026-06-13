package exec

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alyamovsky/drillbit/internal/checks"
	"github.com/alyamovsky/drillbit/internal/config"
)

var base = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func ptrSize(b int64) *config.Size            { s := config.Size(b); return &s }
func ptrDur(d time.Duration) *config.Duration { x := config.Duration(d); return &x }
func ptrBool(b bool) *bool                    { return &b }

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

func dumpdirStep(dir string, l1 *config.L1, now time.Time) StepSpec {
	return StepSpec{
		RunID:  1,
		Drill:  "app-db",
		Level:  "l1",
		Source: config.Source{Name: "dumps", Type: "dumpdir", Path: dir, Pattern: "*.sql.gz", Pick: "newest"},
		L1:     l1,
		Now:    now,
	}
}

func fullL1() *config.L1 {
	return &config.L1{FileMinBytes: ptrSize(1), CompressionTest: ptrBool(true), MaxAge: ptrDur(36 * time.Hour)}
}

func TestRunStepDumpdirL1Pass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-1*time.Hour))

	res, err := NewLocal("h").RunStep(context.Background(), dumpdirStep(dir, fullL1(), base))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s, want pass (%s)", res.Status, res.Summary)
	}
	if len(res.Evidence) != 3 || res.Files != 1 {
		t.Errorf("got %d evidence / %d files, want 3 / 1", len(res.Evidence), res.Files)
	}
	for _, ev := range res.Evidence {
		if ev.Status != checks.Pass {
			t.Errorf("evidence %s = %s, want pass", ev.Kind, ev.Status)
		}
	}
}

func TestRunStepDumpdirL1FailStale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-30*24*time.Hour)) // stale

	res, err := NewLocal("h").RunStep(context.Background(), dumpdirStep(dir, fullL1(), base))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	var maxAge checks.Evidence
	for _, ev := range res.Evidence {
		if ev.Kind == "max_age" {
			maxAge = ev
		}
	}
	if maxAge.Status != checks.Fail {
		t.Errorf("max_age = %s, want fail", maxAge.Status)
	}
}

func TestRunStepDumpdirL1ErrorUnreadable(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope")
	res, err := NewLocal("h").RunStep(context.Background(), dumpdirStep(missing, fullL1(), base))
	if err != nil {
		t.Fatalf("RunStep returned Go error, want StepResult{error}: %v", err)
	}
	if res.Status != checks.Error {
		t.Errorf("status = %s, want error (unreadable dir)", res.Status)
	}
}

func TestRunStepNoFilesIsError(t *testing.T) {
	t.Parallel()
	res, err := NewLocal("h").RunStep(context.Background(), dumpdirStep(t.TempDir(), fullL1(), base))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != checks.Error {
		t.Errorf("status = %s, want error (no matching files)", res.Status)
	}
}

func TestRunStepUnsupported(t *testing.T) {
	t.Parallel()
	cases := []StepSpec{
		{Level: "l2", Source: config.Source{Type: "dumpdir"}},
		{Level: "l1", Source: config.Source{Type: "borg"}},
	}
	for _, step := range cases {
		_, err := NewLocal("h").RunStep(context.Background(), step)
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("(%s,%s) err = %v, want ErrUnsupported", step.Level, step.Source.Type, err)
		}
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	if got := NewLocal("nas").Describe(); got.Host != "nas" {
		t.Errorf("Describe().Host = %q, want nas", got.Host)
	}
}

// The seam's load-bearing invariant: StepSpec/StepResult must stay serializable
// (no func fields, channels, or handles) so a Phase 4 agent can carry them.
func TestStepSpecSerializable(t *testing.T) {
	t.Parallel()
	spec := dumpdirStep("/backups/pg", fullL1(), base)
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal StepSpec: %v", err)
	}
	var got StepSpec
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal StepSpec: %v", err)
	}
	if got.Source.Path != spec.Source.Path || got.L1.FileMinBytes.Bytes() != 1 || !got.Now.Equal(base) {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestStepResultSerializable(t *testing.T) {
	t.Parallel()
	res := StepResult{
		Level:    "l1",
		Status:   checks.Fail,
		Evidence: []checks.Evidence{{Kind: "max_age", Target: "x.sql.gz", Expected: "age <= 36h0m0s", Actual: "age 720h", Status: checks.Fail}},
		Summary:  "fail: 1 checks (0 pass, 1 fail, 0 error)",
		Files:    1,
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal StepResult: %v", err)
	}
	var got StepResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal StepResult: %v", err)
	}
	if got.Status != checks.Fail || len(got.Evidence) != 1 || got.Evidence[0].Kind != "max_age" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestAggregatePrecedence(t *testing.T) {
	t.Parallel()
	ev := func(s checks.Status) checks.Evidence { return checks.Evidence{Status: s} }
	tests := []struct {
		name string
		in   []checks.Evidence
		want checks.Status
	}{
		{"all pass", []checks.Evidence{ev(checks.Pass), ev(checks.Pass)}, checks.Pass},
		{"fail dominates error", []checks.Evidence{ev(checks.Error), ev(checks.Fail)}, checks.Fail},
		{"error over pass", []checks.Evidence{ev(checks.Pass), ev(checks.Error)}, checks.Error},
		{"empty is pass", nil, checks.Pass},
	}
	for _, tt := range tests {
		if got := aggregate(tt.in); got != tt.want {
			t.Errorf("%s: aggregate = %s, want %s", tt.name, got, tt.want)
		}
	}
}
