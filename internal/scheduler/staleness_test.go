package scheduler

import (
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
)

func TestStale(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 14, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	tests := []struct {
		name        string
		maxProofAge time.Duration
		lastProven  time.Time
		want        bool
	}{
		{"fresh proof within SLA", 10 * day, now.Add(-2 * day), false},
		{"proof exactly at SLA edge", 10 * day, now.Add(-10 * day), false},
		{"proof just past SLA", 10 * day, now.Add(-10*day - time.Second), true},
		{"never proven is stale", 10 * day, time.Time{}, true},
		{"no SLA never stale", 0, time.Time{}, false},
		{"negative SLA never stale", -1, now.Add(-100 * day), false},
		// Downtime: no run advanced the proof; staleness fires from the timestamp alone.
		{"downtime past SLA", 7 * day, now.Add(-30 * day), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Stale(tt.maxProofAge, tt.lastProven, now); got != tt.want {
				t.Errorf("Stale(%v, %v, now) = %v, want %v", tt.maxProofAge, tt.lastProven, got, tt.want)
			}
		})
	}
}

func TestHeadlineLevel(t *testing.T) {
	t.Parallel()
	l1, l2, l3 := &config.L1{}, &config.L2{}, &config.L3{}
	tests := []struct {
		name   string
		levels config.Levels
		want   string
	}{
		{"l1 only", config.Levels{L1: l1}, "l1"},
		{"l1+l2", config.Levels{L1: l1, L2: l2}, "l2"},
		{"all three", config.Levels{L1: l1, L2: l2, L3: l3}, "l3"},
		{"l3 only", config.Levels{L3: l3}, "l3"},
		{"none", config.Levels{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := HeadlineLevel(config.Drill{Levels: tt.levels}); got != tt.want {
				t.Errorf("HeadlineLevel(%+v) = %q, want %q", tt.levels, got, tt.want)
			}
		})
	}
}
