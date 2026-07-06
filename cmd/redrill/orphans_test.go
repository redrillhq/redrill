// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"slices"
	"testing"

	"github.com/redrillhq/redrill/internal/config"
)

// A renamed drill orphans its proofs; the startup diff must name exactly the
// orphans, in the store's sorted order.
func TestOrphanedProofs(t *testing.T) {
	t.Parallel()
	drills := []config.Drill{{Name: "app-db"}, {Name: "photos"}}
	for _, tt := range []struct {
		name   string
		proofs []string
		want   []string
	}{
		{"all known", []string{"app-db", "photos"}, nil},
		{"one renamed", []string{"app-db", "old-name", "photos"}, []string{"old-name"}},
		{"all orphaned", []string{"a", "b"}, []string{"a", "b"}},
		{"no proofs", nil, nil},
	} {
		if got := orphanedProofs(drills, tt.proofs); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}
