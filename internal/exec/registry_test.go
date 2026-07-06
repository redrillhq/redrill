// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"slices"
	"sort"
	"testing"

	"github.com/redrillhq/redrill/internal/config"
)

// The check-kind registry has two halves — config's catalog (what validates)
// and exec's builders (what runs). They must enumerate the same kinds: a kind
// config accepts but exec cannot build is the false-pass class the 2026-07-03
// review found (a configured check silently vanishing), and a builder config
// never accepts is dead code.
func TestCheckRegistriesMatchConfig(t *testing.T) {
	t.Parallel()
	l2 := make([]string, 0, len(l2Builders))
	for kind := range l2Builders {
		l2 = append(l2, kind)
	}
	sort.Strings(l2)
	l3 := make([]string, 0, len(l3Builders))
	for kind := range l3Builders {
		l3 = append(l3, kind)
	}
	sort.Strings(l3)

	for _, tt := range []struct {
		level    string
		builders []string
	}{
		{"l2", l2},
		{"l3", l3},
	} {
		want := config.CheckKinds(tt.level)
		if len(want) == 0 {
			t.Fatalf("config.CheckKinds(%q) is empty — the catalog lost a level", tt.level)
		}
		if !slices.Equal(tt.builders, want) {
			t.Errorf("%s registries drifted:\n  config validates: %v\n  exec builds:      %v", tt.level, want, tt.builders)
		}
	}
}
