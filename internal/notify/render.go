// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"fmt"
	"strings"
	"time"
)

const dateLayout = "2006-01-02 15:04 UTC"

// Render returns the notification's title and body, diagnosis first and keeping
// fail visibly distinct from error.
func Render(n Notification) (title, body string) {
	switch n.Event {
	case EventFail:
		title = "redrill: " + n.Drill + " restore FAILED"
		body = verdict(n, "FAILED") +
			"\nThe backup is the problem — a check found bad data.\n" +
			lastGood(n)
	case EventError:
		title = "redrill: " + n.Drill + " drill ERROR"
		body = verdict(n, "ERROR") +
			"\nThe auditor is the problem — redrill could not complete the check; the backup is unproven, not (yet) condemned.\n" +
			lastGood(n)
	case EventStale:
		title = "redrill: " + n.Drill + " proof STALE"
		body = staleVerdict(n) + "\n" + lastGood(n)
	case EventRecover:
		title = "redrill: " + n.Drill + " recovered"
		body = n.Drill + ": passing again" + atLevel(n) + ".\n" + provenNow(n)
	}
	return title, body
}

func verdict(n Notification, word string) string {
	s := n.Drill + ":"
	if n.Level != "" {
		s += " " + strings.ToUpper(n.Level)
	}
	s += " " + word
	if n.Detail != "" {
		s += " — " + n.Detail
	}
	return s + "."
}

func staleVerdict(n Notification) string {
	s := n.Drill + ": proof is STALE"
	if n.MaxProofAge > 0 {
		lvl := ""
		if n.Level != "" {
			lvl = " " + strings.ToUpper(n.Level)
		}
		s += fmt.Sprintf(" — no passing%s drill within the %s SLA", lvl, humanDuration(n.MaxProofAge))
	}
	return s + "."
}

func atLevel(n Notification) string {
	if n.Level == "" {
		return ""
	}
	return " at " + strings.ToUpper(n.Level)
}

func lastGood(n Notification) string {
	if n.LastProven.IsZero() {
		return "Last good proof: never."
	}
	return fmt.Sprintf("Last good proof: %s (%s ago).", n.LastProven.UTC().Format(dateLayout), humanSince(n.Now, n.LastProven))
}

func provenNow(n Notification) string {
	if n.LastProven.IsZero() {
		return "Proof restored just now."
	}
	return "Proof restored: " + n.LastProven.UTC().Format(dateLayout) + "."
}

// humanDuration renders an SLA in the config's own vocabulary (10d, 36h, 30m).
func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return d.String()
	}
}

// humanSince is a coarse age magnitude (one unit of resolution).
func humanSince(now, t time.Time) string {
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
