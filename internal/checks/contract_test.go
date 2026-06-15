// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The false-pass / false-fail contract. For every check kind, an enumerated set
// of cases pinning the EXACT verdict: a fixture that must fail (guarding against
// the cardinal sin — a silent false pass on a dead backup), a near-miss that
// must pass (guarding against false fails / cry-wolf), and the error direction
// where the kind has one. TestContractCoversEveryKind keeps this exhaustive: a
// new check kind has to appear here, in both directions, or CI goes red.

type contractCase struct {
	kind      string
	name      string
	want      Status
	weak      bool   // expected Evidence.Weak
	actualHas string // if set, Evidence.Actual must contain it
	build     func(t *testing.T) (Check, CheckEnv)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// dumpFileEnv writes one dump file (optionally backdated) and returns its path.
func dumpFileEnv(t *testing.T, name, content string, mtime time.Time) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	write(t, p, content)
	if !mtime.IsZero() {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

const day = 24 * time.Hour

var contractCases = []contractCase{
	// --- L1 dump-file checks ---
	{kind: kindFileMinBytes, name: "below-min/fail", want: Fail, build: func(t *testing.T) (Check, CheckEnv) {
		return FileMinBytes{Path: dumpFileEnv(t, "dump.sql.gz", "12345", time.Time{}), Min: 10}, CheckEnv{Now: now}
	}},
	{kind: kindFileMinBytes, name: "exactly-min/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		return FileMinBytes{Path: dumpFileEnv(t, "dump.sql.gz", "0123456789", time.Time{}), Min: 10}, CheckEnv{Now: now}
	}},
	{kind: kindFileMinBytes, name: "missing/error", want: Error, build: func(t *testing.T) (Check, CheckEnv) {
		return FileMinBytes{Path: filepath.Join(t.TempDir(), "missing"), Min: 1}, CheckEnv{Now: now}
	}},

	{kind: kindCompressionTest, name: "plausible-but-not-gzip/fail", want: Fail, build: func(t *testing.T) (Check, CheckEnv) {
		// Looks like a fresh dump (right name, plausible body) but isn't gzip —
		// the "perfect cron, dead backup" class.
		return CompressionTest{Path: dumpFileEnv(t, "dump.sql.gz", "-- a perfectly good looking dump\nSELECT 1;\n", time.Time{})}, CheckEnv{Now: now}
	}},
	{kind: kindCompressionTest, name: "valid-gzip/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		dir := t.TempDir()
		p := filepath.Join(dir, "dump.sql.gz")
		makeGzip(t, p, "SELECT 1;")
		return CompressionTest{Path: p}, CheckEnv{Now: now}
	}},
	{kind: kindCompressionTest, name: "valid-zstd/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		dir := t.TempDir()
		p := filepath.Join(dir, "dump.sql.zst")
		makeZstd(t, p, "SELECT 1;")
		return CompressionTest{Path: p}, CheckEnv{Now: now}
	}},
	{kind: kindCompressionTest, name: "unknown-extension/error", want: Error, build: func(t *testing.T) (Check, CheckEnv) {
		return CompressionTest{Path: dumpFileEnv(t, "dump.sql", "SELECT 1;", time.Time{})}, CheckEnv{Now: now}
	}},

	{kind: kindMaxAge, name: "stale/fail", want: Fail, build: func(t *testing.T) (Check, CheckEnv) {
		return MaxAge{Path: dumpFileEnv(t, "dump.sql.gz", "x", now.Add(-30*day)), Max: 36 * time.Hour}, CheckEnv{Now: now}
	}},
	{kind: kindMaxAge, name: "just-inside-window/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		return MaxAge{Path: dumpFileEnv(t, "dump.sql.gz", "x", now.Add(-35*time.Hour)), Max: 36 * time.Hour}, CheckEnv{Now: now}
	}},
	{kind: kindMaxAge, name: "exact-boundary/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		// age == Max must pass: the window is inclusive (`<=`), not `<`.
		return MaxAge{Path: dumpFileEnv(t, "dump.sql.gz", "x", now.Add(-36*time.Hour)), Max: 36 * time.Hour}, CheckEnv{Now: now}
	}},
	{kind: kindMaxAge, name: "missing/error", want: Error, build: func(t *testing.T) (Check, CheckEnv) {
		return MaxAge{Path: filepath.Join(t.TempDir(), "missing"), Max: time.Hour}, CheckEnv{Now: now}
	}},

	// --- L1 borg checks ---
	{kind: kindSnapshotMaxAge, name: "stale/fail", want: Fail, build: func(_ *testing.T) (Check, CheckEnv) {
		return SnapshotMaxAge{Newest: now.Add(-48 * time.Hour), Max: 36 * time.Hour}, CheckEnv{Now: now}
	}},
	{kind: kindSnapshotMaxAge, name: "just-inside-window/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return SnapshotMaxAge{Newest: now.Add(-35 * time.Hour), Max: 36 * time.Hour}, CheckEnv{Now: now}
	}},
	{kind: kindSnapshotMaxAge, name: "exact-boundary/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return SnapshotMaxAge{Newest: now.Add(-36 * time.Hour), Max: 36 * time.Hour}, CheckEnv{Now: now}
	}},
	{kind: kindSnapshotMaxAge, name: "no-snapshots/error", want: Error, build: func(_ *testing.T) (Check, CheckEnv) {
		return SnapshotMaxAge{Max: 36 * time.Hour}, CheckEnv{Now: now}
	}},

	// size_anomaly is advisory: it always passes and only flags in Actual, so it
	// has no fail direction (TestContractCoversEveryKind exempts it).
	{kind: kindSizeAnomaly, name: "normal/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return SizeAnomaly{LatestSize: 100, TrailingSizes: []int64{100, 100}, Pct: 40}, CheckEnv{}
	}},
	{kind: kindSizeAnomaly, name: "shrunken/pass-but-flagged", want: Pass, actualHas: "ANOMALY", build: func(_ *testing.T) (Check, CheckEnv) {
		return SizeAnomaly{LatestSize: 10, TrailingSizes: []int64{100, 100}, Pct: 40}, CheckEnv{}
	}},

	// --- L2 restore checks ---
	{kind: kindPathExists, name: "missing/fail", want: Fail, build: func(t *testing.T) (Check, CheckEnv) {
		return PathExists{Path: "config/config.php"}, CheckEnv{RestoreDir: t.TempDir(), Now: now}
	}},
	{kind: kindPathExists, name: "present/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		dir := restoreDir(t, map[string]string{"config/config.php": "<?php"})
		return PathExists{Path: "config/config.php"}, CheckEnv{RestoreDir: dir, Now: now}
	}},

	{kind: kindPathAbsent, name: "present/fail", want: Fail, build: func(t *testing.T) (Check, CheckEnv) {
		dir := restoreDir(t, map[string]string{"leftover.tmp": "x"})
		return PathAbsent{Path: "leftover.tmp"}, CheckEnv{RestoreDir: dir, Now: now}
	}},
	{kind: kindPathAbsent, name: "absent/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		return PathAbsent{Path: "leftover.tmp"}, CheckEnv{RestoreDir: t.TempDir(), Now: now}
	}},

	{kind: kindCanaryFile, name: "missing/fail-weak", want: Fail, weak: true, build: func(t *testing.T) (Check, CheckEnv) {
		return CanaryFile{Path: "canary"}, CheckEnv{RestoreDir: t.TempDir(), Now: now}
	}},
	{kind: kindCanaryFile, name: "present/pass-weak", want: Pass, weak: true, build: func(t *testing.T) (Check, CheckEnv) {
		dir := restoreDir(t, map[string]string{"canary": "ok"})
		return CanaryFile{Path: "canary"}, CheckEnv{RestoreDir: dir, Now: now}
	}},

	{kind: kindHashMatch, name: "mismatch/fail", want: Fail, build: func(t *testing.T) (Check, CheckEnv) {
		dir := restoreDir(t, map[string]string{"a.txt": "hello"})
		return HashMatch{Manifest: map[string]string{"a.txt": sha256Hex("not hello")}}, CheckEnv{RestoreDir: dir, Now: now}
	}},
	{kind: kindHashMatch, name: "match/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		dir := restoreDir(t, map[string]string{"a.txt": "hello"})
		return HashMatch{Manifest: map[string]string{"a.txt": sha256Hex("hello")}}, CheckEnv{RestoreDir: dir, Now: now}
	}},
	{kind: kindHashMatch, name: "engine-verified-empty-manifest/pass", want: Pass, build: func(t *testing.T) (Check, CheckEnv) {
		return HashMatch{}, CheckEnv{RestoreDir: t.TempDir(), Now: now}
	}},
	{kind: kindHashMatch, name: "manifest-file-missing/error", want: Error, build: func(t *testing.T) (Check, CheckEnv) {
		return HashMatch{Manifest: map[string]string{"gone.txt": sha256Hex("x")}}, CheckEnv{RestoreDir: t.TempDir(), Now: now}
	}},

	{kind: kindNewestFileMaxAge, name: "stale/fail", want: Fail, build: func(_ *testing.T) (Check, CheckEnv) {
		return NewestFileMaxAge{Newest: now.Add(-10 * day), Max: 8 * day}, CheckEnv{Now: now}
	}},
	{kind: kindNewestFileMaxAge, name: "just-inside-window/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return NewestFileMaxAge{Newest: now.Add(-8*day + time.Hour), Max: 8 * day}, CheckEnv{Now: now}
	}},
	{kind: kindNewestFileMaxAge, name: "exact-boundary/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return NewestFileMaxAge{Newest: now.Add(-8 * day), Max: 8 * day}, CheckEnv{Now: now}
	}},
	{kind: kindNewestFileMaxAge, name: "nothing-restored/error", want: Error, build: func(_ *testing.T) (Check, CheckEnv) {
		return NewestFileMaxAge{Max: 8 * day}, CheckEnv{Now: now}
	}},

	{kind: kindMinTotalBytes, name: "too-small/fail", want: Fail, build: func(_ *testing.T) (Check, CheckEnv) {
		return MinTotalBytes{Total: 50, Min: 100}, CheckEnv{}
	}},
	{kind: kindMinTotalBytes, name: "exactly-min/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return MinTotalBytes{Total: 100, Min: 100}, CheckEnv{}
	}},

	{kind: kindFileCountTolerance, name: "way-off/fail", want: Fail, build: func(_ *testing.T) (Check, CheckEnv) {
		return FileCountTolerance{Count: 50, Prev: 100, Pct: 15}, CheckEnv{}
	}},
	{kind: kindFileCountTolerance, name: "at-tolerance-boundary/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return FileCountTolerance{Count: 85, Prev: 100, Pct: 15}, CheckEnv{}
	}},
	{kind: kindFileCountTolerance, name: "no-baseline/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return FileCountTolerance{Count: 5, Prev: 0, Pct: 15}, CheckEnv{}
	}},

	// --- L3 sandbox checks (fake sandbox, no Docker) ---
	{kind: kindSQL, name: "predicate-false/fail", want: Fail, build: func(_ *testing.T) (Check, CheckEnv) {
		return SQL{Query: "select count(*) from users", Expect: "> 0"}, CheckEnv{Sandbox: fakeSandbox{out: "0", exit: 0}, Now: now}
	}},
	{kind: kindSQL, name: "predicate-true/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return SQL{Query: "select count(*) from users", Expect: "> 0"}, CheckEnv{Sandbox: fakeSandbox{out: "42", exit: 0}, Now: now}
	}},
	{kind: kindSQL, name: "query-errors/error", want: Error, build: func(_ *testing.T) (Check, CheckEnv) {
		return SQL{Query: "select boom", Expect: "> 0"}, CheckEnv{Sandbox: fakeSandbox{out: "", exit: 1}, Now: now}
	}},
	{kind: kindSQL, name: "uncoercible-value/error", want: Error, build: func(_ *testing.T) (Check, CheckEnv) {
		return SQL{Query: "select name from users limit 1", Expect: "> 0"}, CheckEnv{Sandbox: fakeSandbox{out: "alice", exit: 0}, Now: now}
	}},

	{kind: kindSQLNoError, name: "query-errors/fail", want: Fail, build: func(_ *testing.T) (Check, CheckEnv) {
		return SQLNoError{Query: "select * from missing"}, CheckEnv{Sandbox: fakeSandbox{exit: 1}, Now: now}
	}},
	{kind: kindSQLNoError, name: "query-ok/pass", want: Pass, build: func(_ *testing.T) (Check, CheckEnv) {
		return SQLNoError{Query: "select * from orders limit 1"}, CheckEnv{Sandbox: fakeSandbox{exit: 0}, Now: now}
	}},
}

func TestContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range contractCases {
		t.Run(tc.kind+"/"+tc.name, func(t *testing.T) {
			c, env := tc.build(t)
			ev, err := c.Run(ctx, env)
			if err != nil {
				t.Fatalf("Run returned a hard error (must produce Evidence instead): %v", err)
			}
			if ev.Kind != tc.kind {
				t.Errorf("evidence Kind = %q, want %q", ev.Kind, tc.kind)
			}
			if ev.Status != tc.want {
				t.Errorf("status = %q, want %q (expected %q, actual %q)", ev.Status, tc.want, ev.Expected, ev.Actual)
			}
			if ev.Weak != tc.weak {
				t.Errorf("weak = %v, want %v", ev.Weak, tc.weak)
			}
			if tc.actualHas != "" && !strings.Contains(ev.Actual, tc.actualHas) {
				t.Errorf("actual %q does not contain %q", ev.Actual, tc.actualHas)
			}
		})
	}
}

// TestContractCoversEveryKind makes the contract exhaustive: every catalog kind
// needs a must-pass case (and, unless advisory, a must-fail case), and no case
// may name a kind outside the catalog.
func TestContractCoversEveryKind(t *testing.T) {
	t.Parallel()
	all := []string{
		kindFileMinBytes, kindCompressionTest, kindMaxAge,
		kindSnapshotMaxAge, kindSizeAnomaly,
		kindPathExists, kindPathAbsent, kindCanaryFile, kindHashMatch,
		kindNewestFileMaxAge, kindMinTotalBytes, kindFileCountTolerance,
		kindSQL, kindSQLNoError,
	}
	advisory := map[string]bool{kindSizeAnomaly: true} // always passes; no fail direction

	known := map[string]bool{}
	for _, k := range all {
		known[k] = true
	}
	haveFail, havePass := map[string]bool{}, map[string]bool{}
	for _, tc := range contractCases {
		if !known[tc.kind] {
			t.Errorf("contract case %q names kind %q, absent from the catalog list — add it", tc.name, tc.kind)
		}
		switch tc.want {
		case Fail:
			haveFail[tc.kind] = true
		case Pass:
			havePass[tc.kind] = true
		}
	}
	for _, k := range all {
		if !havePass[k] {
			t.Errorf("check kind %q has no must-pass case (false-fail / cry-wolf direction)", k)
		}
		if !advisory[k] && !haveFail[k] {
			t.Errorf("check kind %q has no must-fail case (false-pass direction — the cardinal sin)", k)
		}
	}
}
