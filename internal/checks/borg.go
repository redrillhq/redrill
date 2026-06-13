package checks

import (
	"context"
	"fmt"
	"time"
)

// L1 archive (borg/restic) check kinds (DESIGN §6, §7). native_check is engine
// delegation built by the executor from the driver's Report; these two are pure
// predicates the executor feeds with data pulled from the engine.

const (
	kindSnapshotMaxAge = "snapshot_max_age"
	kindSizeAnomaly    = "size_anomaly"
)

// SnapshotMaxAge fails if the newest snapshot is older than Max — a stale source
// whose backups stopped advancing.
type SnapshotMaxAge struct {
	Newest time.Time
	Max    time.Duration
}

func (c SnapshotMaxAge) Kind() string { return kindSnapshotMaxAge }

func (c SnapshotMaxAge) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindSnapshotMaxAge, Expected: fmt.Sprintf("newest age <= %s", c.Max)}
	if c.Newest.IsZero() {
		ev.Status, ev.Actual = Error, "no snapshots to age-check"
		return ev, nil
	}
	ev.Target = c.Newest.UTC().Format(time.RFC3339)
	age := env.Now.Sub(c.Newest)
	ev.Actual = fmt.Sprintf("age %s", age.Round(time.Second))
	ev.Status = Fail
	if age <= c.Max {
		ev.Status = Pass
	}
	return ev, nil
}

// SizeAnomaly is advisory (DESIGN §6: "→ warn"): it always passes, but flags
// when the latest snapshot is more than Pct% below the trailing average — a
// possible sign of silent data loss. No trailing history, or a zero average,
// yields no signal.
type SizeAnomaly struct {
	LatestSize    int64
	TrailingSizes []int64
	Pct           int
}

func (c SizeAnomaly) Kind() string { return kindSizeAnomaly }

func (c SizeAnomaly) Run(_ context.Context, _ CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindSizeAnomaly, Status: Pass, Expected: fmt.Sprintf("latest within %d%% of trailing avg", c.Pct)}
	if len(c.TrailingSizes) == 0 {
		ev.Actual = "insufficient history"
		return ev, nil
	}
	var sum int64
	for _, s := range c.TrailingSizes {
		sum += s
	}
	avg := float64(sum) / float64(len(c.TrailingSizes))
	if avg <= 0 {
		ev.Actual = "trailing average is zero — no signal"
		return ev, nil
	}
	if float64(c.LatestSize) < avg*(1-float64(c.Pct)/100) {
		below := (1 - float64(c.LatestSize)/avg) * 100
		ev.Actual = fmt.Sprintf("ANOMALY: latest %d is %.0f%% below trailing avg %.0f", c.LatestSize, below, avg)
	} else {
		ev.Actual = fmt.Sprintf("latest %d vs trailing avg %.0f (ok)", c.LatestSize, avg)
	}
	return ev, nil
}
