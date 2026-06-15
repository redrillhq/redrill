// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/store"
)

func TestWriteExposition(t *testing.T) {
	t.Parallel()
	families := []metricFamily{
		{"redrill_proof_sla_ok", "1 if within SLA.", "gauge", []labeledValue{
			{labels: [][2]string{{"drill", "b"}}, value: "0"},
			{labels: [][2]string{{"drill", "a"}}, value: "1"}, // out of order on purpose
		}},
		{"redrill_scratch_used_bytes", "scratch bytes.", "gauge", []labeledValue{{value: "0"}}},
	}
	var buf bytes.Buffer
	writeExposition(&buf, families)
	want := `# HELP redrill_proof_sla_ok 1 if within SLA.
# TYPE redrill_proof_sla_ok gauge
redrill_proof_sla_ok{drill="a"} 1
redrill_proof_sla_ok{drill="b"} 0
# HELP redrill_scratch_used_bytes scratch bytes.
# TYPE redrill_scratch_used_bytes gauge
redrill_scratch_used_bytes 0
`
	if buf.String() != want {
		t.Errorf("exposition mismatch:\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

func TestEscapeLabelValue(t *testing.T) {
	t.Parallel()
	got := formatLabels([][2]string{{"path", "a\"b\\c\nd"}})
	want := `{path="a\"b\\c\nd"}`
	if got != want {
		t.Errorf("formatLabels = %q, want %q", got, want)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := context.Background()
	proofAt := testNow.Add(-24 * time.Hour)
	seedFinishedRun(t, s.store, "app-db", store.ResultPass, testNow.Add(-time.Hour))
	if err := s.store.RecordProof(ctx, "app-db", "l1", proofAt); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s.Handler(), http.MethodGet, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
	body := rec.Body.String()
	wants := []string{
		"# TYPE redrill_last_proven_timestamp_seconds gauge",
		fmt.Sprintf(`redrill_last_proven_timestamp_seconds{drill="app-db",level="l1"} %d`, proofAt.Unix()),
		`redrill_proof_sla_ok{drill="app-db"} 1`,
		`redrill_run_result{drill="app-db",result="pass"} 1`,
		`redrill_run_result{drill="app-db",result="fail"} 0`,
		`redrill_run_result{drill="app-db",result="error"} 0`,
		`redrill_run_duration_seconds{drill="app-db"} 1.5`,
		"# TYPE redrill_bytes_restored_total counter",
		`redrill_bytes_restored_total{drill="app-db"} 1000`,
		"redrill_scratch_used_bytes 0",
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("metrics missing line %q\n--- body ---\n%s", w, body)
		}
	}
}

// A never-proven drill must read as out of SLA (0), the metric mirror of stale.
func TestMetricsStaleWhenNeverProven(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rec := do(t, s.Handler(), http.MethodGet, "/metrics")
	if !strings.Contains(rec.Body.String(), `redrill_proof_sla_ok{drill="app-db"} 0`) {
		t.Errorf("never-proven drill should report sla_ok 0:\n%s", rec.Body.String())
	}
}

func TestDirSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.bin"), make([]byte, 50), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dirSize(dir); got != 150 {
		t.Errorf("dirSize = %d, want 150", got)
	}
	if got := dirSize(filepath.Join(dir, "nope")); got != 0 {
		t.Errorf("dirSize of missing dir = %d, want 0", got)
	}
	if got := dirSize(""); got != 0 {
		t.Errorf("dirSize of empty path = %d, want 0", got)
	}
}
