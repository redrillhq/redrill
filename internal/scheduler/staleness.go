// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package scheduler

import (
	"time"

	"github.com/redrillhq/redrill/internal/config"
)

// Stale fires on a proof existing, not on drills running, so it fires even when
// the daemon was down. A never-proven drill is stale; a non-positive maxProofAge
// means no SLA.
func Stale(maxProofAge time.Duration, lastProven, now time.Time) bool {
	if maxProofAge <= 0 {
		return false
	}
	if lastProven.IsZero() {
		return true
	}
	return now.Sub(lastProven) > maxProofAge
}

// HeadlineLevel returns the highest configured level (l3 > l2 > l1, "" if none);
// the Proof SLA is measured against its proof age.
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
