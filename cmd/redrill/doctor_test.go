// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// setupDoctorConfig writes a config with one dumpdir source pointing at dumpPath.
func setupDoctorConfig(t *testing.T, dumpPath string) string {
	t.Helper()
	tmp := t.TempDir()
	body := fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources:
  - {name: dumps, type: dumpdir, path: %s, pattern: "*.sql.gz"}
drills:
  - name: app-db
    source: dumps
    schedule: "Sun 05:00"
    levels:
      l1: {max_age: 36h}
`, filepath.Join(tmp, "data"), filepath.Join(tmp, "scratch"), dumpPath)
	return writeConfig(t, body)
}

func TestDoctor(t *testing.T) {
	t.Parallel()

	t.Run("healthy env exits 0", func(t *testing.T) {
		t.Parallel()
		cfg := setupDoctorConfig(t, t.TempDir()) // a real, readable dump dir
		var out, errb bytes.Buffer
		if code := run([]string{"doctor", "-c", cfg}, &out, &errb); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)\n%s", code, errb.String(), out.String())
		}
		s := out.String()
		if !strings.Contains(s, "repo: dumps") || !strings.Contains(s, "scratch") {
			t.Errorf("want repo and scratch checks, got:\n%s", s)
		}
		if strings.Contains(s, "ERROR") {
			t.Errorf("healthy env should have no ERROR rows:\n%s", s)
		}
	})

	t.Run("unreachable repo exits 2", func(t *testing.T) {
		t.Parallel()
		cfg := setupDoctorConfig(t, "/no/such/backup/dir")
		var out, errb bytes.Buffer
		if code := run([]string{"doctor", "-c", cfg}, &out, &errb); code != 2 {
			t.Fatalf("exit = %d, want 2 (infra error)\n%s", code, out.String())
		}
		if !strings.Contains(out.String(), "ERROR") {
			t.Errorf("want an ERROR row for the unreachable repo, got:\n%s", out.String())
		}
	})

	t.Run("invalid config exits 3", func(t *testing.T) {
		t.Parallel()
		cfg := writeConfig(t, "version: 1\nscratch: {dir: /s}\n") // missing data_dir
		var out, errb bytes.Buffer
		if code := run([]string{"doctor", "-c", cfg}, &out, &errb); code != 3 {
			t.Fatalf("exit = %d, want 3 (config error)", code)
		}
	})

	t.Run("restic is a real engine: missing binary or unreachable repo errors", func(t *testing.T) {
		t.Parallel()
		// Deterministic regardless of host: if restic is absent the binary check
		// errors; if present, the bogus repo is unreachable — either way, exit 2.
		tmp := t.TempDir()
		cfg := writeConfig(t, fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources:
  - {name: dumps, type: dumpdir, path: %s, pattern: "*.sql.gz"}
  - {name: photos, type: restic, repo: "/no/such/restic/repo", password_env: RESTIC_PW}
drills:
  - {name: app-db, source: dumps, schedule: "Sun 05:00", levels: {l1: {max_age: 36h}}}
`, filepath.Join(tmp, "data"), filepath.Join(tmp, "scratch"), t.TempDir()))
		var out, errb bytes.Buffer
		if code := run([]string{"doctor", "-c", cfg}, &out, &errb); code != 2 {
			t.Fatalf("exit = %d, want 2 (restic now a real engine)\n%s", code, out.String())
		}
		if !strings.Contains(out.String(), "ERROR") {
			t.Errorf("want an ERROR row for restic, got:\n%s", out.String())
		}
	})
}

func TestEngineVersionRangeCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		vr   versionRange
		line string
		ok   bool
	}{
		{"borg 1.4 ok", borgVersions, "borg 1.4.4", true},
		{"borg 1.2 ok (min)", borgVersions, "borg 1.2.0", true},
		{"borg 1.1 too old", borgVersions, "borg 1.1.18", false},
		{"borg 2.x unverified", borgVersions, "borg 2.0.0", false},
		{"restic 0.18 ok", resticVersions, "restic 0.18.1 compiled with go1.26.3 on linux/arm64", true},
		{"restic 0.17 ok (min)", resticVersions, "restic 0.17.0", true},
		{"restic 0.16 too old", resticVersions, "restic 0.16.4", false},
		{"unparseable is left alone", borgVersions, "borg (unknown build)", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := tt.vr.check(tt.line); ok != tt.ok {
				t.Errorf("check(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			}
		})
	}
}

func TestDoctorJSON(t *testing.T) {
	t.Parallel()
	cfg := setupDoctorConfig(t, "/no/such/backup/dir")
	var out, errb bytes.Buffer
	if code := run([]string{"doctor", "-c", cfg, "--json"}, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	var got struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Check  string `json:"check"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v (%q)", err, out.String())
	}
	if got.OK {
		t.Errorf("ok = true, want false (unreachable repo)")
	}
	if len(got.Checks) == 0 {
		t.Fatalf("want checks in JSON output")
	}
}
