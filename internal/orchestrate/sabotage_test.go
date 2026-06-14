//go:build sabotage

package orchestrate

import (
	"os"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/checks"
	"github.com/alyamovsky/redrill/internal/store"
)

// Sabotage kit: each fixture is a "perfect cron, dead backup" redrill must flag.
// Built here, not committed, because they depend on mtime, which git drops.

func writeRaw(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// assertCaught fails unless some byKinds check returned fail.
func assertCaught(t *testing.T, res RunResult, byKinds ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, k := range byKinds {
		want[k] = true
	}
	for _, lv := range res.Levels {
		for _, ev := range lv.Evidence {
			if want[ev.Kind] && ev.Status == checks.Fail {
				return
			}
		}
	}
	t.Errorf("no failing check among %v caught the sabotage; levels = %+v", byKinds, res.Levels)
}

// empty-dump: 0-byte file with a plausible name and fresh mtime;
// file_min_bytes and compression_test must catch it.
func TestSabotageEmptyDump(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRaw(t, dir+"/app-1.sql.gz", "", base) // 0-byte "dump", fresh
	st := newStore(t)
	drill, src := drillFor(dir, l1Full())

	res := runDrill(t, st, drill, src, RunOptions{})
	if res.Status != store.ResultFail {
		t.Fatalf("empty-dump: result = %s, want fail", res.Status)
	}
	assertCaught(t, res, "file_min_bytes", "compression_test")
}

// stale-source: a valid dump but 30 days old; only max_age should flag it.
func TestSabotageStaleSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1; -- a perfectly valid dump", base.Add(-30*24*time.Hour))
	st := newStore(t)
	drill, src := drillFor(dir, l1Full())

	res := runDrill(t, st, drill, src, RunOptions{})
	if res.Status != store.ResultFail {
		t.Fatalf("stale-source: result = %s, want fail", res.Status)
	}
	assertCaught(t, res, "max_age")
}
