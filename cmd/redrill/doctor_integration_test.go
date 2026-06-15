// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package main

import (
	"bytes"
	"fmt"
	osexec "os/exec"
	"path/filepath"
	"testing"
)

// With the real borg binary present, an unreachable repo is an error → exit 2.
// This proves doctor's exit codes against a broken env using a real engine.
func TestDoctorBorgUnreachableRepoIntegration(t *testing.T) {
	if _, err := osexec.LookPath("borg"); err != nil {
		t.Skip("borg not installed")
	}
	tmp := t.TempDir()
	cfg := writeConfig(t, fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources:
  - {name: arch, type: borg, repo: %s}
drills:
  - {name: d, source: arch, schedule: "Sun 04:10", levels: {l1: {native_check: true}}}
`, filepath.Join(tmp, "data"), filepath.Join(tmp, "scratch"), filepath.Join(tmp, "nonexistent-repo")))

	var out, errb bytes.Buffer
	if code := run([]string{"doctor", "-c", cfg}, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2 (borg present, repo unreachable)\n%s", code, out.String())
	}
}

// With the real restic binary present, an unreachable repo is an error → exit 2.
func TestDoctorResticUnreachableRepoIntegration(t *testing.T) {
	if _, err := osexec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	tmp := t.TempDir()
	cfg := writeConfig(t, fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources:
  - {name: repo, type: restic, repo: %s, password_env: RESTIC_PW}
drills:
  - {name: d, source: repo, schedule: "Sun 04:10", levels: {l1: {native_check: true}}}
`, filepath.Join(tmp, "data"), filepath.Join(tmp, "scratch"), filepath.Join(tmp, "nonexistent-repo")))

	var out, errb bytes.Buffer
	if code := run([]string{"doctor", "-c", cfg}, &out, &errb); code != 2 {
		t.Fatalf("exit = %d, want 2 (restic present, repo unreachable)\n%s", code, out.String())
	}
}
