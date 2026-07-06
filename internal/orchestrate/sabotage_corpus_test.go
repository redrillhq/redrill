// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build sabotage

package orchestrate

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/fixtures"
	"github.com/redrillhq/redrill/internal/store"
)

// Broadened sabotage corpus (beyond the canonical six in sabotage_test.go):
// distinct "perfect cron, dead backup" corruption modes. Each is a
// plausible-looking dump — right name, fresh mtime, non-trivial size — whose
// bytes are dead, and each must be caught as fail (never a silent pass, never an
// auditor error). These are pure-Go dumpdir L1 fixtures; they all fall to
// compression_test, proving it catches the whole class, not a single shape.

func runCorpusFail(t *testing.T, raw []byte) RunResult {
	t.Helper()
	dir := fixtures.Dump(t, fixtures.DumpRaw(raw))
	st := newStore(t)
	drill, src := drillFor(dir, l1Full())
	return runDrill(t, st, drill, src, RunOptions{})
}

// truncated-dump: a valid gzip cut off mid-stream — the trailing CRC/length is
// gone, so the file looks fine until you try to read all of it.
func TestSabotageTruncatedDump(t *testing.T) {
	t.Parallel()
	gz := fixtures.GzipBytes(t, strings.Repeat("SELECT * FROM users WHERE id = 12345;\n", 200))
	res := runCorpusFail(t, gz[:len(gz)-8])
	mustFail(t, res, "truncated-dump")
	assertCaught(t, res, "compression_test")
}

// corrupted-stream: a valid gzip header with a flipped byte in the deflate body
// — bit-rot that the header alone can't reveal.
func TestSabotageCorruptedStream(t *testing.T) {
	t.Parallel()
	gz := fixtures.GzipBytes(t, strings.Repeat("SELECT * FROM users WHERE id = 12345;\n", 200))
	gz[11] ^= 0xFF // first byte past the 10-byte gzip header
	res := runCorpusFail(t, gz)
	mustFail(t, res, "corrupted-stream")
	assertCaught(t, res, "compression_test")
}

// magic-mismatch: a file named *.sql.gz that actually holds a zstd frame (the
// cron compressed it with the wrong tool), so the extension-keyed gzip test
// rejects it.
func TestSabotageMagicMismatch(t *testing.T) {
	t.Parallel()
	res := runCorpusFail(t, fixtures.ZstdBytes(t, "SELECT 1; -- compressed with the wrong tool"))
	mustFail(t, res, "magic-mismatch")
	assertCaught(t, res, "compression_test")
}

// execL2 is an L2 drill whose only assertion is the operator-script escape
// hatch: the restored dump must be non-empty.
func execL2() config.Levels {
	return config.Levels{L2: &config.L2{
		Restore: config.Restore{Scope: "full"},
		Checks:  []config.Check{{Kind: "exec", Exec: `set -- *.sql.gz; test -s "$1"`}},
	}}
}

// exec-dead-restore: the escape-hatch check kind must catch a dead backup like
// any built-in (invariant #5: every check kind ships a sabotage fixture). A
// 0-byte dump with a plausible name and fresh mtime restores at L2; the
// operator script asserts it non-empty and its exit code is the verdict.
func TestSabotageExecCheck(t *testing.T) {
	t.Parallel()
	dir := fixtures.Dump(t, fixtures.DumpRaw(nil))
	st := newStore(t)
	drill, src := drillFor(dir, execL2())
	res := runDrill(t, st, drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	mustFail(t, res, "exec-dead-restore")
	assertCaught(t, res, "exec")
}

// The cry-wolf mirror: a healthy restore must pass the same script.
func TestExecCheckNearPass(t *testing.T) {
	t.Parallel()
	dir := fixtures.Dump(t)
	st := newStore(t)
	drill, src := drillFor(dir, execL2())
	res := runDrill(t, st, drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if res.Status != store.ResultPass {
		t.Fatalf("healthy exec drill = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
}

// photoDrill is the file_count habitat: a directory of jpegs restored in full
// (dumpdir all-matching-window), asserted "> 50 non-empty JPEGs".
func photoDrill(dir string) (config.Drill, config.Source) {
	src := config.Source{Name: "photos", Type: "dumpdir", Path: dir, Pattern: "*.jpg", Pick: "all-matching-window"}
	minSize := config.Size(1)
	levels := config.Levels{L2: &config.L2{
		Restore: config.Restore{Scope: "full"},
		Checks: []config.Check{{Kind: "file_count", FileCount: &config.FileCountCheck{
			Glob: "*.jpg", MinSize: minSize, Expect: "> 50",
		}}},
	}}
	return config.Drill{Name: "photos", Source: "photos", Levels: levels}, src
}

// writePhotos builds a photo export: filled jpegs plus empty ones — right
// names, fresh mtimes, plausible listing.
func writePhotos(t *testing.T, filled, empty int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < filled; i++ {
		writeFile(t, dir, fmt.Sprintf("img-%03d.jpg", i), "jpeg-bytes-jpeg-bytes")
	}
	for i := 0; i < empty; i++ {
		writeFile(t, dir, fmt.Sprintf("img-e%03d.jpg", i), "")
	}
	return dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// filecount-dead-export: the "perfect cron, dead backup" class for file trees
// — the export produced the right file names but empty bytes. 60 jpegs
// restore, only 5 hold data; "> 50 non-empty" must fail (invariant #5: every
// check kind ships a sabotage fixture).
func TestSabotageFileCountEmptyExport(t *testing.T) {
	t.Parallel()
	dir := writePhotos(t, 5, 55)
	st := newStore(t)
	drill, src := photoDrill(dir)
	res := runDrill(t, st, drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	mustFail(t, res, "filecount-dead-export")
	assertCaught(t, res, "file_count")
}

// entropyDrill audits a directory of plain .sql dumps with the advisory
// encryption canary.
func entropyDrill(dir string) (config.Drill, config.Source) {
	src := config.Source{Name: "dumps", Type: "dumpdir", Path: dir, Pattern: "*.sql", Pick: "all-matching-window"}
	levels := config.Levels{L2: &config.L2{
		Restore: config.Restore{Scope: "full"},
		Checks: []config.Check{
			{Kind: "min_total_bytes", MinTotalBytes: config.Size(1)},
			{Kind: "entropy_anomaly", EntropyAnomaly: true},
		},
	}}
	return config.Drill{Name: "dumps", Source: "dumps", Levels: levels}, src
}

// randomBytes is deterministic near-max-entropy content (chained sha256).
func randomBytes(n int) string {
	out := make([]byte, 0, n)
	block := sha256.Sum256([]byte("redrill-corpus-entropy"))
	for len(out) < n {
		out = append(out, block[:]...)
		block = sha256.Sum256(block[:])
	}
	return string(out[:n])
}

// entropy-encrypted-restore: text-class files that come back as random bytes
// raise the ANOMALY flag. Advisory kinds are exempt from the fail gate
// (size_anomaly precedent): the run PASSES either way — the flag in the
// evidence is the whole signal, and the healthy mirror stays quiet.
func TestEntropyAnomalyFlagsEncryptedRestore(t *testing.T) {
	t.Parallel()
	healthy := t.TempDir()
	for i := 0; i < 10; i++ {
		writeFile(t, healthy, fmt.Sprintf("dump-%02d.sql", i),
			strings.Repeat("INSERT INTO users VALUES (1, 'user', 'user@example.test');\n", 20))
	}
	st := newStore(t)
	drill, src := entropyDrill(healthy)
	res := runDrill(t, st, drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if res.Status != store.ResultPass {
		t.Fatalf("healthy = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
	if flagged, ev := entropyEvidence(res); flagged {
		t.Errorf("healthy restore flagged: %q", ev)
	}

	encrypted := t.TempDir()
	for i := 0; i < 10; i++ {
		writeFile(t, encrypted, fmt.Sprintf("dump-%02d.sql", i), randomBytes(4096))
	}
	drill, src = entropyDrill(encrypted)
	res = runDrill(t, newStore(t), drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if res.Status != store.ResultPass {
		t.Fatalf("encrypted = %s, want pass (advisory never fails); levels = %+v", res.Status, res.Levels)
	}
	flagged, ev := entropyEvidence(res)
	if !flagged {
		t.Fatalf("encrypted restore not flagged; evidence = %q", ev)
	}
}

// entropyEvidence returns whether the entropy_anomaly evidence carries the
// ANOMALY flag, plus its text (asserting weak along the way).
func entropyEvidence(res RunResult) (bool, string) {
	for _, lv := range res.Levels {
		for _, ev := range lv.Evidence {
			if ev.Kind == "entropy_anomaly" && ev.Weak {
				return strings.Contains(ev.Actual, "ANOMALY"), ev.Actual
			}
		}
	}
	return false, "(no weak entropy_anomaly evidence)"
}

// The cry-wolf mirror: 60 healthy jpegs pass the same assertion.
func TestFileCountNearPass(t *testing.T) {
	t.Parallel()
	dir := writePhotos(t, 60, 0)
	st := newStore(t)
	drill, src := photoDrill(dir)
	res := runDrill(t, st, drill, src, RunOptions{Scratch: config.Scratch{Dir: t.TempDir()}})
	if res.Status != store.ResultPass {
		t.Fatalf("healthy photo drill = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
}
