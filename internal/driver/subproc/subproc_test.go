// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package subproc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A ctx kill surfaces as the runner's error, never as an engine exit code (a
// timed-out native check would otherwise read as "backup corrupt"). This is
// the guarantee every CLI engine inherits from the shared runner.
func TestExecRunnerCtxCancelIsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, exit, err := ExecRunner(ctx, "", nil, "sleep", []string{"5"})
	if err == nil {
		t.Fatalf("ExecRunner under expired ctx: err = nil (exit %d), want ctx error", exit)
	}
}

func TestExecRunnerExitAndOutput(t *testing.T) {
	t.Parallel()
	stdout, _, exit, err := ExecRunner(context.Background(), "", nil, "sh", []string{"-c", "echo hi"})
	if err != nil || exit != 0 || strings.TrimSpace(string(stdout)) != "hi" {
		t.Fatalf("echo: stdout=%q exit=%d err=%v", stdout, exit, err)
	}
	_, _, exit, err = ExecRunner(context.Background(), "", nil, "sh", []string{"-c", "exit 3"})
	if err != nil || exit != 3 {
		t.Fatalf("exit 3: exit=%d err=%v (a non-zero exit is not a runner error)", exit, err)
	}
	_, _, _, err = ExecRunner(context.Background(), "", nil, "definitely-not-a-binary", nil)
	if err == nil {
		t.Fatal("unstartable process: err = nil, want start error")
	}
}

// Output formats both failure classes with the caller's label: runner errors
// wrapped, non-zero exits with the first stderr line.
func TestOutput(t *testing.T) {
	t.Parallel()
	fake := func(_ context.Context, _ string, _ []string, _ string, args []string) ([]byte, []byte, int, error) {
		switch args[0] {
		case "ok":
			return []byte("data"), nil, 0, nil
		case "bad":
			return nil, []byte("first line\nsecond"), 2, nil
		default:
			return nil, nil, -1, errors.New("boom")
		}
	}
	if out, err := Output(context.Background(), fake, "", nil, "x", []string{"ok"}, "x ok"); err != nil || string(out) != "data" {
		t.Fatalf("ok: out=%q err=%v", out, err)
	}
	_, err := Output(context.Background(), fake, "", nil, "x", []string{"bad"}, "x bad")
	if err == nil || err.Error() != "x bad: exit 2: first line" {
		t.Fatalf("bad: err = %v, want label + exit + first stderr line", err)
	}
	_, err = Output(context.Background(), fake, "", nil, "x", []string{"boom"}, "x boom")
	if err == nil || !strings.Contains(err.Error(), "x boom: boom") {
		t.Fatalf("boom: err = %v, want wrapped runner error", err)
	}
}

func TestEnvInheritsAndAppends(t *testing.T) {
	t.Setenv("SUBPROC_TEST_MARKER", "yes")
	env := Env("EXTRA_KEY=v")
	var marker, extra bool
	for _, kv := range env {
		switch kv {
		case "SUBPROC_TEST_MARKER=yes":
			marker = true
		case "EXTRA_KEY=v":
			extra = true
		}
	}
	if !marker || !extra {
		t.Fatalf("env missing inherited (%v) or extra (%v) entry", marker, extra)
	}
}

func TestDirReport(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a": "12345", "sub/b": "123"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := DirReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Files != 2 || rep.Bytes != 8 {
		t.Errorf("report = %+v, want 2 files / 8 bytes", rep)
	}
}

func TestOneLine(t *testing.T) {
	t.Parallel()
	if got := OneLine([]byte("  first\nsecond\n")); got != "first" {
		t.Errorf("OneLine = %q", got)
	}
	if got := OneLine(nil); got != "" {
		t.Errorf("OneLine(nil) = %q", got)
	}
}
