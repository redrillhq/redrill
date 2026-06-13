package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSnapshotMaxAge(t *testing.T) {
	t.Parallel()
	env := CheckEnv{Now: now}

	fresh, err := SnapshotMaxAge{Newest: now.Add(-1 * time.Hour), Max: 36 * time.Hour}.Run(context.Background(), env)
	if err != nil || fresh.Status != Pass {
		t.Errorf("fresh: status %s err %v, want pass", fresh.Status, err)
	}
	stale, _ := SnapshotMaxAge{Newest: now.Add(-30 * 24 * time.Hour), Max: 36 * time.Hour}.Run(context.Background(), env)
	if stale.Status != Fail {
		t.Errorf("stale: status %s, want fail", stale.Status)
	}
	none, _ := SnapshotMaxAge{Max: 36 * time.Hour}.Run(context.Background(), env)
	if none.Status != Error {
		t.Errorf("no snapshots: status %s, want error", none.Status)
	}
}

func TestSizeAnomaly(t *testing.T) {
	t.Parallel()
	env := CheckEnv{Now: now}

	// 1.2MB latest against ~5MB trailing avg, 40% threshold → anomaly (still passes).
	an, _ := SizeAnomaly{LatestSize: 1_200_000, TrailingSizes: []int64{5_000_000, 5_200_000, 4_800_000}, Pct: 40}.Run(context.Background(), env)
	if an.Status != Pass {
		t.Errorf("anomaly status = %s, want pass (advisory)", an.Status)
	}
	if !strings.Contains(an.Actual, "ANOMALY") {
		t.Errorf("anomaly Actual = %q, want it flagged", an.Actual)
	}

	ok, _ := SizeAnomaly{LatestSize: 4_900_000, TrailingSizes: []int64{5_000_000, 5_200_000}, Pct: 40}.Run(context.Background(), env)
	if ok.Status != Pass || strings.Contains(ok.Actual, "ANOMALY") {
		t.Errorf("normal size: %+v, want pass without anomaly", ok)
	}

	empty, _ := SizeAnomaly{LatestSize: 100, Pct: 40}.Run(context.Background(), env)
	if empty.Status != Pass || !strings.Contains(empty.Actual, "insufficient") {
		t.Errorf("no history: %+v, want pass/insufficient", empty)
	}

	zero, _ := SizeAnomaly{LatestSize: 100, TrailingSizes: []int64{0, 0}, Pct: 40}.Run(context.Background(), env)
	if zero.Status != Pass || strings.Contains(zero.Actual, "ANOMALY") {
		t.Errorf("zero avg: %+v, want pass without anomaly", zero)
	}
}
