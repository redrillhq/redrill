//go:build integration

package orchestrate

import (
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/fixtures"
	"github.com/alyamovsky/redrill/internal/store"
)

// Real-engine happy-path and cry-wolf (near-pass) drills — the must-pass side of
// the driver×level fail/near-pass contract. The fail side is the sabotage kit
// (sabotage_test.go + sabotage_corpus_test.go); shared setup is in
// engine_helpers_test.go. Pairs:
//
//	dumpdir L1  pass: a fresh valid dump             fail: empty / stale / truncated / corrupted / magic-mismatch
//	dumpdir L3  pass: loads + asserts; older major   fail: wrong-db / version-trap
//	borg    L1  pass: valid repo, snapshot in window  fail: stale-source / truncated-segment
//	borg    L2  pass: all expected paths present      fail: missing-data-dir
//	borg    L3  pass: dump extracted + booted         fail: shares loadAndCheck with dumpdir L3

func TestIntegrationBorgL1L2(t *testing.T) {
	fixtures.RequireBorg(t)
	repo, passFile := fixtures.Borg(t)
	res := runBorgDrill(t, repo, passFile, borgL1L2Drill())
	if res.Status != store.ResultPass {
		t.Fatalf("L1+L2 borg drill = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
	if res.LevelReached != "l2" {
		t.Errorf("level reached %s, want l2", res.LevelReached)
	}
}

func TestIntegrationDumpdirL3(t *testing.T) {
	rt := requireDocker(t)
	dir := fixtures.Dump(t, fixtures.DumpBody("CREATE TABLE users(id int);\nINSERT INTO users VALUES (1),(2),(3);\n"))
	res := runL3Drill(t, rt, dir, pgImage(), []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
		{Kind: "sql_no_error", SQLNoError: "select * from users limit 1"},
	})
	if res.Status != store.ResultPass {
		t.Fatalf("dumpdir L3 = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
}

// older-pg-major (the non-trap direction): a dump from an OLDER major loads into
// a newer sandbox, so redrill must not cry version-trap. The mirror of
// TestSabotageVersionTrap.
func TestIntegrationDumpdirL3OlderMajorLoads(t *testing.T) {
	rt := requireDocker(t)
	dir := fixtures.Dump(t, fixtures.DumpBody("-- Dumped from database version 14\nCREATE TABLE users(id int);\nINSERT INTO users VALUES (1),(2),(3);\n"))
	res := runL3Drill(t, rt, dir, pgImage(), []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
	})
	if res.Status != store.ResultPass {
		t.Fatalf("older-major dump L3 = %s, want pass (no version trap downward); levels = %+v", res.Status, res.Levels)
	}
}

// borg L1 near-pass: a snapshot just inside the recency window passes — the
// boundary mirror of TestSabotageStaleSourceBorg's 30-day archive.
func TestIntegrationBorgL1NearPass(t *testing.T) {
	fixtures.RequireBorg(t)
	repo, passFile := fixtures.Borg(t, fixtures.BorgArchiveAge(35*time.Hour)) // window is 36h
	res := runBorgDrill(t, repo, passFile, borgStaleDrill())
	if res.Status != store.ResultPass {
		t.Fatalf("borg L1 near-pass = %s, want pass (35h archive within a 36h window); levels = %+v", res.Status, res.Levels)
	}
}

// borg L3 (the M11 gap): a Postgres dump lives inside a borg archive; redrill
// extracts it via extract_path, boots a sandbox, and asserts against it —
// alongside the dumpdir-L3 path. Needs both borg and Docker.
func TestIntegrationBorgL3(t *testing.T) {
	fixtures.RequireBorg(t)
	rt := requireDocker(t)
	repo, passFile := fixtures.Borg(t, fixtures.BorgFile("db.dump",
		"CREATE TABLE users(id int);\nINSERT INTO users VALUES (1),(2),(3);\n"))
	res := runBorgL3Drill(t, rt, repo, passFile, "db.dump", pgImage(), []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
		{Kind: "sql_no_error", SQLNoError: "select * from users limit 1"},
	})
	if res.Status != store.ResultPass {
		t.Fatalf("borg L3 = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
}
