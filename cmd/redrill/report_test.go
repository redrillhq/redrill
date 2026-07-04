// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/store"
)

func TestReportMarkdown(t *testing.T) {
	t.Parallel()
	cfgPath, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		recordRun(t, st, "app-db", store.ResultPass, time.Now().UTC().Add(-time.Hour), "l1")
		if err := st.RecordProof(context.Background(), "app-db", "l1", time.Now().UTC().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	})

	var out, errb bytes.Buffer
	if code := run([]string{"report", "-c", cfgPath}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{"# Redrill proof report", "1 of 1 datasets proven within SLA", "## app-db — within SLA", "PASS"} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown missing %q:\n%s", want, got)
		}
	}
}

func TestReportHTMLAndOut(t *testing.T) {
	t.Parallel()
	cfgPath, _ := setupStatusConfig(t)
	outFile := filepath.Join(t.TempDir(), "report.html")

	var out, errb bytes.Buffer
	if code := run([]string{"report", "-c", cfgPath, "--format", "html", "--out", outFile}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errb.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout not empty with --out: %q", out.String())
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<title>Redrill proof report</title>") {
		t.Errorf("html file missing title:\n%.200s", data)
	}
	if !strings.Contains(errb.String(), "report written to") {
		t.Errorf("stderr missing confirmation: %q", errb.String())
	}
}

func TestReportJSON(t *testing.T) {
	t.Parallel()
	cfgPath, _ := setupStatusConfig(t)
	var out, errb bytes.Buffer
	if code := run([]string{"report", "-c", cfgPath, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errb.String())
	}
	var rep struct {
		Total  int `json:"total"`
		Drills []struct {
			Name string `json:"name"`
		} `json:"drills"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("--json output does not parse: %v\n%s", err, out.String())
	}
	if rep.Total != 1 || len(rep.Drills) != 1 || rep.Drills[0].Name != "app-db" {
		t.Errorf("report json = %+v", rep)
	}
}

func TestReportBadFlags(t *testing.T) {
	t.Parallel()
	cfgPath, _ := setupStatusConfig(t)
	var out, errb bytes.Buffer
	if code := run([]string{"report", "-c", cfgPath, "--format", "pdf"}, &out, &errb); code != 2 {
		t.Fatalf("bad format: exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "must be md or html") {
		t.Errorf("stderr = %q", errb.String())
	}

	errb.Reset()
	if code := run([]string{"report", "-c", "missing.yaml"}, &out, &errb); code != 3 {
		t.Fatalf("missing config: exit = %d, want 3", code)
	}
}
