// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"errors"
	"testing"
)

// A predicate that parses must evaluate without panicking, and any evaluation
// error must be a coercion error — so the caller maps it to check `error`, never
// a silent fail/pass. Parsing is also stable: the canonical form re-parses to
// itself.
func FuzzParseExpect(f *testing.F) {
	for _, s := range []string{
		"> 0", ">= 100", "== 5", "!= 3", "between 1 10",
		"age < 8d", "age > 36h", "matches ^v[0-9]+$", "nonempty",
	} {
		f.Add(s, "42")
	}
	f.Add("", "")
	f.Add("age < 8d", "2026-01-02T03:04:05Z")
	f.Add("between 1 2 3", "1.5")

	f.Fuzz(func(t *testing.T, s, actual string) {
		e, err := ParseExpect(s)
		if err != nil {
			return // rejected predicates are fine
		}
		if _, eerr := e.Evaluate(actual, now); eerr != nil && !errors.Is(eerr, ErrCoercion) {
			t.Fatalf("Evaluate(%q) on predicate %q: non-coercion error %v", actual, s, eerr)
		}
		e2, err2 := ParseExpect(e.String())
		if err2 != nil {
			t.Fatalf("re-parse of canonical %q failed: %v", e.String(), err2)
		}
		if e2.String() != e.String() {
			t.Fatalf("String() unstable: %q -> %q", e.String(), e2.String())
		}
	})
}
