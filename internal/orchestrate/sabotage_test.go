// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build sabotage

package orchestrate

import (
	"os"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/checks"
	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/fixtures"
	"github.com/redrillhq/redrill/internal/store"
)

// The sabotage kit: each fixture is a "perfect cron, dead backup" that engines'
// own checks pass and redrill must flag. Every test asserts the exact verdict
// (fail — a bad backup, never error) and the catching check. Engine-backed
// fixtures skip when borg or Docker is absent; the maintainer's CI provides
// both, so the gate blocks there.

// mustFail asserts the run flagged a bad backup (fail), keeping fail distinct
// from error (the auditor's own problem).
func mustFail(t *testing.T, res RunResult, fixture string) {
	t.Helper()
	if res.Status != store.ResultFail {
		t.Fatalf("%s: result = %s, want fail (a bad backup, not an auditor error); levels = %+v",
			fixture, res.Status, res.Levels)
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

// empty-dump: a 0-byte file with a plausible name and fresh mtime (dumpdir L1).
func TestSabotageEmptyDump(t *testing.T) {
	t.Parallel()
	dir := fixtures.Dump(t, fixtures.DumpRaw(nil))
	st := newStore(t)
	drill, src := drillFor(dir, l1Full())

	res := runDrill(t, st, drill, src, RunOptions{})
	mustFail(t, res, "empty-dump")
	assertCaught(t, res, "file_min_bytes", "compression_test")
}

// stale-source (dumpdir L1 max_age): a valid dump 30 days old.
func TestSabotageStaleSourceDumpdir(t *testing.T) {
	t.Parallel()
	dir := fixtures.Dump(t, fixtures.DumpBody("SELECT 1; -- a perfectly valid dump"), fixtures.DumpAge(30*24*time.Hour))
	st := newStore(t)
	drill, src := drillFor(dir, l1Full())

	res := runDrill(t, st, drill, src, RunOptions{})
	mustFail(t, res, "stale-source (dumpdir)")
	assertCaught(t, res, "max_age")
}

// stale-source (borg L1 snapshot_max_age): newest archive 30 days old.
func TestSabotageStaleSourceBorg(t *testing.T) {
	fixtures.RequireBorg(t)
	repo, passFile := fixtures.Borg(t, fixtures.BorgArchiveAge(30*24*time.Hour))
	res := runBorgDrill(t, repo, passFile, borgStaleDrill())
	mustFail(t, res, "stale-source (borg)")
	assertCaught(t, res, "snapshot_max_age")
}

// truncated-segment (borg L1 native check): the one corruption engines catch —
// proves redrill delegates integrity to borg check correctly.
func TestSabotageTruncatedSegment(t *testing.T) {
	fixtures.RequireBorg(t)
	repo, passFile := fixtures.Borg(t)

	seg := largestFile(t, repo+"/data")
	info, err := os.Stat(seg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(seg, info.Size()-64); err != nil {
		t.Fatal(err)
	}

	res := runBorgDrill(t, repo, passFile, borgNativeOnlyDrill())
	mustFail(t, res, "truncated-segment")
	assertCaught(t, res, "native_check")
}

// missing-data-dir (borg L2 path_exists): a bad exclude dropped data/.
func TestSabotageMissingDataDir(t *testing.T) {
	fixtures.RequireBorg(t)
	repo, passFile := fixtures.Borg(t, fixtures.BorgOmitData())
	res := runBorgDrill(t, repo, passFile, borgL1L2Drill())
	mustFail(t, res, "missing-data-dir")
	assertCaught(t, res, "path_exists")
}

// restic-stale-source (restic L1 snapshot_max_age): newest snapshot 30 days old.
func TestSabotageStaleSourceRestic(t *testing.T) {
	fixtures.RequireRestic(t)
	repo, passFile := fixtures.Restic(t, fixtures.ResticSnapshotAge(30*24*time.Hour))
	res := runResticDrill(t, repo, passFile, resticStaleDrill())
	mustFail(t, res, "restic-stale-source")
	assertCaught(t, res, "snapshot_max_age")
}

// restic-missing-pack (restic L1 native check): a deleted pack file — restic's
// own check must flag it, proving redrill delegates integrity to restic check.
func TestSabotageMissingPackRestic(t *testing.T) {
	fixtures.RequireRestic(t)
	repo, passFile := fixtures.Restic(t)

	if err := os.Remove(largestFile(t, repo+"/data")); err != nil {
		t.Fatal(err)
	}

	res := runResticDrill(t, repo, passFile, resticNativeOnlyDrill())
	mustFail(t, res, "restic-missing-pack")
	assertCaught(t, res, "native_check")
}

// restic-missing-data-dir (restic L2 path_exists): a bad exclude dropped data/.
func TestSabotageMissingDataDirRestic(t *testing.T) {
	fixtures.RequireRestic(t)
	repo, passFile := fixtures.Restic(t, fixtures.ResticOmitData())
	res := runResticDrill(t, repo, passFile, resticL1L2Drill())
	mustFail(t, res, "restic-missing-data-dir")
	assertCaught(t, res, "path_exists")
}

// wrong-db-dump (dumpdir L3 sql): a valid dump of the wrong/empty database.
func TestSabotageWrongDBDump(t *testing.T) {
	rt := requireDocker(t)
	dir := fixtures.Dump(t, fixtures.DumpBody("CREATE TABLE users(id int);\n")) // table exists, but no rows
	res := runL3Drill(t, rt, dir, pgImage(), []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
	})
	mustFail(t, res, "wrong-db-dump")
	assertCaught(t, res, "sql")
}

// version-trap (dumpdir L3 load): a dump needing a newer pg major than the sandbox.
func TestSabotageVersionTrap(t *testing.T) {
	rt := requireDocker(t)
	dir := fixtures.Dump(t, fixtures.DumpBody("-- Dumped from database version 99.0\nCREATE TABLE users(id int);\nINSERT INTO users VALUES (1);\n"))
	res := runL3Drill(t, rt, dir, pgImage(), []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
	})
	mustFail(t, res, "version-trap")
	assertCaught(t, res, "load")
}
