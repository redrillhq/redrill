// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "update golden files")

func TestRenderGolden(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	proven := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC) // 21 days earlier
	tests := []struct {
		name string
		note Notification
	}{
		{"fail", Notification{Event: EventFail, Drill: "nextcloud-db", Level: "l3", Detail: "sql oc_users: expected > 0, got 0", LastProven: proven, Now: now}},
		{"error", Notification{Event: EventError, Drill: "nextcloud-db", Level: "l3", Detail: "load: exec: container exited before ready", LastProven: proven, Now: now}},
		{"error_never", Notification{Event: EventError, Drill: "app-db", Level: "l1", Detail: "dumpdir /backups/pg: no such file or directory", Now: now}},
		{"stale", Notification{Event: EventStale, Drill: "nextcloud-files", Level: "l2", MaxProofAge: 10 * 24 * time.Hour, LastProven: proven, Now: now}},
		{"stale_never", Notification{Event: EventStale, Drill: "new-drill", Level: "l3", MaxProofAge: 10 * 24 * time.Hour, Now: now}},
		{"recover", Notification{Event: EventRecover, Drill: "nextcloud-files", Level: "l2", LastProven: now, Now: now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			title, body := Render(tt.note)
			got := title + "\n\n" + body + "\n"
			golden := filepath.Join("testdata", tt.name+".golden")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("rendered output mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", tt.name, got, want)
			}
		})
	}
}

// fail and error must be visibly distinct: a glance tells the operator whether
// the backup or the auditor is the problem.
func TestFailErrorVisiblyDistinct(t *testing.T) {
	t.Parallel()
	common := Notification{Drill: "db", Level: "l3", Detail: "x", Now: time.Now()}
	failNote, errNote := common, common
	failNote.Event = EventFail
	errNote.Event = EventError
	ft, fb := Render(failNote)
	et, eb := Render(errNote)
	if ft == et {
		t.Errorf("titles identical: %q", ft)
	}
	if fb == eb {
		t.Error("bodies identical")
	}
	if !strings.Contains(ft, "FAILED") || !strings.Contains(et, "ERROR") {
		t.Errorf("titles not distinct: fail=%q error=%q", ft, et)
	}
	if !strings.Contains(fb, "The backup is the problem") {
		t.Errorf("fail body missing backup-fault framing: %q", fb)
	}
	if !strings.Contains(eb, "auditor is the problem") {
		t.Errorf("error body missing auditor-fault framing: %q", eb)
	}
}
