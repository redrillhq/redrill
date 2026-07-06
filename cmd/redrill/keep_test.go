// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keepConfig builds a dumpdir L2 drill whose restore lands in scratch — the
// thing --keep preserves.
func keepConfig(t *testing.T) (cfgPath, scratchDir string) {
	t.Helper()
	tmp := t.TempDir()
	backups := filepath.Join(tmp, "backups")
	scratchDir = filepath.Join(tmp, "scratch")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte("create table t (id int);\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backups, "app.sql.gz"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources:
  - {name: dumps, type: dumpdir, path: %s, pattern: "*.sql.gz"}
drills:
  - name: app-db
    source: dumps
    levels:
      l2:
        restore: {scope: full}
        checks:
          - min_total_bytes: 1
`, filepath.Join(tmp, "data"), scratchDir, backups)
	return writeConfig(t, body), scratchDir
}

func TestRunKeepPreservesScratchAndHints(t *testing.T) {
	t.Parallel()
	cfgPath, scratchDir := keepConfig(t)
	var out, errb bytes.Buffer
	if code := run([]string{"run", "app-db", "--keep", "-c", cfgPath}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "kept: scratch restore at ") {
		t.Errorf("missing scratch hint:\n%s", got)
	}
	if !strings.Contains(got, "removed at the next `redrill serve` start") {
		t.Errorf("missing ephemerality note:\n%s", got)
	}
	kept := filepath.Join(scratchDir, "run-1")
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("kept scratch %s missing: %v", kept, err)
	}
}

func TestRunKeepJSON(t *testing.T) {
	t.Parallel()
	cfgPath, scratchDir := keepConfig(t)
	var out, errb bytes.Buffer
	if code := run([]string{"run", "app-db", "--keep", "--json", "-c", cfgPath}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errb.String())
	}
	var res struct {
		KeptScratch string `json:"kept_scratch"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if res.KeptScratch != filepath.Join(scratchDir, "run-1") {
		t.Errorf("kept_scratch = %q", res.KeptScratch)
	}
}

// Without --keep, nothing survives and no hints print.
func TestRunWithoutKeepCleansUp(t *testing.T) {
	t.Parallel()
	cfgPath, scratchDir := keepConfig(t)
	var out, errb bytes.Buffer
	if code := run([]string{"run", "app-db", "-c", cfgPath}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errb.String())
	}
	if strings.Contains(out.String(), "kept:") {
		t.Errorf("keep hints printed without --keep:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(scratchDir, "run-1")); !os.IsNotExist(err) {
		t.Errorf("scratch survived without --keep (err=%v)", err)
	}
}
