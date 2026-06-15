// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package scheduler

import (
	"testing"
	"time"
)

func TestParseScheduleErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"bad weekday", "Xyz 04:10"},
		{"hour out of range", "Sun 24:10"},
		{"garbage", "not a schedule"},
		{"too few cron fields", "10 4 *"},
		{"bad cron field", "99 4 * * 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseSchedule(tt.spec); err == nil {
				t.Errorf("ParseSchedule(%q) = nil error, want error", tt.spec)
			}
		})
	}
}

func TestParseScheduleNext(t *testing.T) {
	t.Parallel()
	// A Wednesday at 12:00 UTC.
	now := time.Date(2026, time.June, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		spec       string
		wantWeekly time.Weekday // -1 if not asserted
		wantHour   int
		wantMinute int
	}{
		{"weekday shorthand", "Sun 04:10", time.Sunday, 4, 10},
		{"daily shorthand", "05:30", -1, 5, 30},
		{"single-digit hour", "Mon 4:05", time.Monday, 4, 5},
		{"raw cron weekly", "10 4 * * 0", time.Sunday, 4, 10},
		{"raw cron daily", "0 6 * * *", -1, 6, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, err := ParseSchedule(tt.spec)
			if err != nil {
				t.Fatalf("ParseSchedule(%q): %v", tt.spec, err)
			}
			next := s.Next(now).UTC()
			if !next.After(now) {
				t.Errorf("Next(%v) = %v, not after now", now, next)
			}
			if tt.wantWeekly >= 0 && next.Weekday() != tt.wantWeekly {
				t.Errorf("Next weekday = %v, want %v (next=%v)", next.Weekday(), tt.wantWeekly, next)
			}
			if next.Hour() != tt.wantHour || next.Minute() != tt.wantMinute {
				t.Errorf("Next = %02d:%02d, want %02d:%02d", next.Hour(), next.Minute(), tt.wantHour, tt.wantMinute)
			}
		})
	}
}

// Identical instants in different zones yield the same next fire.
func TestScheduleNextIsUTC(t *testing.T) {
	t.Parallel()
	s, err := ParseSchedule("Sun 04:10")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.June, 10, 12, 0, 0, 0, time.UTC)
	plus5 := base.In(time.FixedZone("UTC+5", 5*3600))
	if got, want := s.Next(plus5).UTC(), s.Next(base).UTC(); !got.Equal(want) {
		t.Errorf("Next differs by input zone: %v vs %v", got, want)
	}
}

func TestScheduleString(t *testing.T) {
	t.Parallel()
	s, err := ParseSchedule("Sun 04:10")
	if err != nil {
		t.Fatal(err)
	}
	if s.String() != "Sun 04:10" {
		t.Errorf("String() = %q, want %q", s.String(), "Sun 04:10")
	}
}
