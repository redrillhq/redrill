// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build sabotage

package orchestrate

import (
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
