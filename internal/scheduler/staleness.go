package scheduler

import (
	"time"

	"github.com/alyamovsky/redrill/internal/config"
)

// Stale reports whether a drill's proof has aged past its Proof SLA. It is a
// pure function of timestamps (DESIGN §9.8): the SLA is on a proof *existing*,
// not on drills running, so staleness fires even when the daemon was down — no
// run or event is needed to detect it. A never-proven drill is stale (absence of
// proof is itself the alert, DESIGN §3 story 1). A non-positive maxProofAge
// means no SLA, so the drill is never stale.
func Stale(maxProofAge time.Duration, lastProven, now time.Time) bool {
	if maxProofAge <= 0 {
		return false
	}
	if lastProven.IsZero() {
		return true
	}
	return now.Sub(lastProven) > maxProofAge
}

// HeadlineLevel returns the highest level a drill configures (l3 > l2 > l1), or
// "" if none. A drill's headline proof age — the number shown for it and the one
// the Proof SLA is measured against — is this level's (DESIGN §6).
func HeadlineLevel(d config.Drill) string {
	switch {
	case d.Levels.L3 != nil:
		return "l3"
	case d.Levels.L2 != nil:
		return "l2"
	case d.Levels.L1 != nil:
		return "l1"
	default:
		return ""
	}
}
