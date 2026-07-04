// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const EventDigest Event = "weekly_digest"

// Digest is the weekly proof summary: every dataset, its proof age, and the
// bytes the last proof verified.
type Digest struct {
	Now     time.Time
	Entries []DigestEntry
}

type DigestEntry struct {
	Drill       string
	Level       string    // headline level
	LastProven  time.Time // zero = never proven
	Stale       bool
	MaxProofAge time.Duration // 0 = no SLA
	LastResult  string        // "" = never ran
	Bytes       int64         // bytes restored by the last run
}

// RenderDigest returns the digest's title and body, one line per dataset.
func RenderDigest(d Digest) (title, body string) {
	ok := 0
	for _, e := range d.Entries {
		if !e.Stale {
			ok++
		}
	}
	title = fmt.Sprintf("redrill: weekly digest — %d of %d proven within SLA", ok, len(d.Entries))
	var b strings.Builder
	fmt.Fprintf(&b, "Proof digest, %s.\n\n", d.Now.UTC().Format(dateLayout))
	for _, e := range d.Entries {
		b.WriteString(digestLine(e, d.Now) + "\n")
	}
	return title, b.String()
}

func digestLine(e DigestEntry, now time.Time) string {
	s := e.Drill + ": "
	if e.LastProven.IsZero() {
		s += "never proven"
	} else {
		s += fmt.Sprintf("proven %s ago (%s)", humanSince(now, e.LastProven), strings.ToUpper(e.Level))
	}
	switch {
	case e.Stale && e.MaxProofAge > 0:
		s += fmt.Sprintf(" · STALE (SLA %s)", humanDuration(e.MaxProofAge))
	case e.Stale:
		s += " · STALE"
	case e.MaxProofAge > 0:
		s += " · ok"
	default:
		s += " · no SLA"
	}
	switch {
	case e.LastResult == "pass" && e.Bytes > 0:
		s += " · " + humanBytes(e.Bytes) + " verified"
	case e.LastResult != "" && e.LastResult != "pass":
		s += " · last run: " + e.LastResult
	}
	return s
}

// DispatchDigest renders and sends the digest when weekly_digest is enabled.
// Like Dispatch, a failed send is logged, never returned.
func (n *Notifier) DispatchDigest(ctx context.Context, d Digest) {
	if !n.DigestEnabled() {
		return
	}
	title, body := RenderDigest(d)
	if err := n.sender.Send(ctx, title, body); err != nil {
		n.log.Warn("notification send failed", "event", string(EventDigest), "error", err.Error())
	}
}

// DigestEnabled reports whether weekly_digest is among the enabled events, so
// callers can skip assembling the digest data entirely.
func (n *Notifier) DigestEnabled() bool {
	return n != nil && n.enabled[EventDigest]
}

// humanBytes is IEC, one decimal under 10 units.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	suffix := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}[exp]
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, suffix)
	}
	return fmt.Sprintf("%.0f %s", v, suffix)
}
