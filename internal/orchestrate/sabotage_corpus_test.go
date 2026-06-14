//go:build sabotage

package orchestrate

import (
	"strings"
	"testing"

	"github.com/alyamovsky/redrill/internal/fixtures"
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
