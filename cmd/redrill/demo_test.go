// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/redrillhq/redrill/internal/config"
)

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func mustStat(t *testing.T, path string) fs.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// The whole demo, end to end without a sandbox: healthy PASS, sabotaged FAIL,
// exit 0. This is the launch asset's happy path.
func TestDemoSabotageEndToEnd(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := run([]string{"demo", "sabotage", "--no-sandbox"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr: %s\nstdout: %s", code, errb.String(), out.String())
	}
	got := out.String()
	for _, want := range []string{
		"[1/3] drill against the healthy backup",
		"result: PASS",
		"[2/3] sabotage",
		"[3/3] drill against the sabotaged backup",
		"FAIL  file_min_bytes",
		"result: FAIL — the dead backup was caught",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("demo output missing %q", want)
		}
	}
	// The healthy PASS must come before the sabotaged FAIL.
	if strings.Index(got, "result: PASS") > strings.Index(got, "result: FAIL") {
		t.Error("PASS and FAIL out of order")
	}
}

func TestDemoUsage(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := run([]string{"demo"}, &out, &errb); code != 2 {
		t.Fatalf("bare demo: exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "redrill demo sabotage") {
		t.Errorf("usage missing: %q", errb.String())
	}
	if code := run([]string{"demo", "chaos"}, &out, &errb); code != 2 {
		t.Fatalf("unknown demo: exit = %d, want 2", code)
	}
}

// The rendered demo config must validate in both shapes — the same trust
// guarantee init gives.
func TestDemoConfigValidates(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	for _, sandbox := range []bool{false, true} {
		if _, err := config.Parse([]byte(demoConfigYAML(ws, sandbox))); err != nil {
			t.Errorf("sandbox=%v: %v", sandbox, err)
		}
	}
}

// The healthy dump must stay above the drill's file_min_bytes and gunzip to
// the SQL that seeds it.
func TestDemoSQLSize(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/dump.sql.gz"
	if err := writeDemoDump(path); err != nil {
		t.Fatal(err)
	}
	f, err := gzip.NewReader(mustOpen(t, path))
	if err != nil {
		t.Fatal(err)
	}
	sql, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sql, []byte("CREATE TABLE users")) {
		t.Error("dump missing schema")
	}
	if n := bytes.Count(sql, []byte("INSERT INTO users")); n != demoUsers {
		t.Errorf("dump has %d inserts, want %d", n, demoUsers)
	}
	if fi := mustStat(t, path); fi.Size() < 2048 {
		t.Errorf("gzip only %d bytes — too close to the 1KiB file_min_bytes", fi.Size())
	}
}
