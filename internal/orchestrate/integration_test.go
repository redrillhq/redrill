//go:build integration

package orchestrate

import (
	"testing"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/fixtures"
	"github.com/alyamovsky/redrill/internal/store"
)

// Real-engine happy-path drills. The "perfect cron, dead backup" sabotage
// fixtures live in sabotage_test.go; shared setup is in engine_helpers_test.go.

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
	res := runL3Drill(t, rt, dir, "postgres:16", []config.Check{
		{Kind: "sql", SQL: &config.SQLCheck{Query: "select count(*) from users", Expect: "> 0"}},
		{Kind: "sql_no_error", SQLNoError: "select * from users limit 1"},
	})
	if res.Status != store.ResultPass {
		t.Fatalf("dumpdir L3 = %s, want pass; levels = %+v", res.Status, res.Levels)
	}
}
