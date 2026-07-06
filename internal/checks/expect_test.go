// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"errors"
	"testing"
	"time"
)

func TestParseExpectErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pred string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"bare number", "100"},
		{"unknown operator", "~ 5"},
		{"gt no operand", ">"},
		{"gt non-number", "> abc"},
		{"gt extra token", "> 1 2"},
		{"between one arg", "between 1"},
		{"between three args", "between 1 2 3"},
		{"between non-number", "between a b"},
		{"age missing duration", "age <"},
		{"age bad comparator", "age <= 5h"},
		{"age bad duration", "age < 5x"},
		{"age negative", "age < -5h"},
		{"age extra token", "age < 5h plus"},
		{"matches no regex", "matches"},
		{"matches bad regex", "matches ("},
		{"nonempty with operand", "nonempty x"},
		{"no-space operator", ">5"},
		{"no-space age", "age<8d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseExpect(tt.pred); err == nil {
				t.Fatalf("ParseExpect(%q) = nil error, want error", tt.pred)
			}
		})
	}
}

func TestParseExpectValid(t *testing.T) {
	t.Parallel()
	for _, pred := range []string{
		"> 100", ">= 0", "== 42", "!= -1", "between 10 20.5",
		"age < 8d", "age > 36h", "matches ^oc_users$", "matches a b", "nonempty",
	} {
		t.Run(pred, func(t *testing.T) {
			t.Parallel()
			e, err := ParseExpect(pred)
			if err != nil {
				t.Fatalf("ParseExpect(%q): %v", pred, err)
			}
			if e.String() != pred {
				t.Errorf("String() = %q, want original %q", e.String(), pred)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	const day = 24 * time.Hour

	tests := []struct {
		name   string
		pred   string
		actual string
		want   bool
		coerce bool // expect an ErrCoercion error (-> check Error)
	}{
		{"gt pass", "> 100", "150", true, false},
		{"gt fail", "> 100", "100", false, false},
		{"gt coerce", "> 100", "lots", false, true},
		{"ge boundary", ">= 100", "100", true, false},
		{"ge fail", ">= 100", "99", false, false},
		{"eq pass", "== 42", "42", true, false},
		{"eq pass float", "== 42", "42.0", true, false},
		{"eq fail", "== 42", "43", false, false},
		{"ne pass", "!= 0", "1", true, false},
		{"ne fail", "!= 0", "0", false, false},
		{"between in", "between 10 20", "15", true, false},
		{"between low edge", "between 10 20", "10", true, false},
		{"between high edge", "between 10 20", "20", true, false},
		{"between out", "between 10 20", "21", false, false},
		{"between coerce", "between 10 20", "x", false, true},
		{"age< fresh", "age < 8d", at(-3 * day), true, false},
		{"age< stale", "age < 8d", at(-10 * day), false, false},
		{"age< exact", "age < 8d", at(-8 * day), false, false}, // age==8d is not < 8d
		{"age< coerce", "age < 8d", "not-a-time", false, true},
		{"age> old", "age > 8d", at(-10 * day), true, false},
		{"age> recent", "age > 8d", at(-3 * day), false, false},
		{"matches hit", "matches ok.*", "ok-123", true, false},
		{"matches miss", "matches ok.*", "bad", false, false},
		{"matches space", "matches a b", "a b", true, false},
		{"matches anchored", "matches ok", "not ok", false, false}, // full match, not substring
		{"matches alternation anchored", "matches a|b", "xay", false, false},
		{"nonempty yes", "nonempty", "x", true, false},
		{"nonempty no", "nonempty", "", false, false},
		{"nonempty blank", "nonempty", "   ", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := ParseExpect(tt.pred)
			if err != nil {
				t.Fatalf("ParseExpect(%q): %v", tt.pred, err)
			}
			got, err := e.Evaluate(tt.actual, now)
			if tt.coerce {
				if !errors.Is(err, ErrCoercion) {
					t.Fatalf("Evaluate(%q, %q) err = %v, want ErrCoercion", tt.pred, tt.actual, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Evaluate(%q, %q) unexpected err: %v", tt.pred, tt.actual, err)
			}
			if got != tt.want {
				t.Errorf("Evaluate(%q, %q) = %v, want %v", tt.pred, tt.actual, got, tt.want)
			}
		})
	}
}

// Integer counts above 2^53 must compare exactly — float64 == silently
// collides adjacent values there (2026-06-14 review deferral, fixed with
// big.Rat arithmetic).
func TestEvaluateExactIntegers(t *testing.T) {
	t.Parallel()
	e, err := ParseExpect("== 9007199254740993") // 2^53 + 1
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Evaluate("9007199254740993", time.Time{}); !got {
		t.Error("exact match rejected")
	}
	if got, _ := e.Evaluate("9007199254740992", time.Time{}); got {
		t.Error("2^53 collision: adjacent integer compared equal")
	}
	ne, err := ParseExpect("!= 9007199254740992")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ne.Evaluate("9007199254740993", time.Time{}); !got {
		t.Error("2^53 collision: != missed a real difference")
	}
}

// A future timestamp has no honest age: small clock skew is forgiven, real
// future is a coercion error — never a silent pass of `age <`.
func TestEvaluateFutureTimestamp(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	e, err := ParseExpect("age < 8d")
	if err != nil {
		t.Fatal(err)
	}

	// 2 minutes ahead: within skew tolerance, treated as age 0 → passes.
	got, err := e.Evaluate(now.Add(2*time.Minute).Format(time.RFC3339), now)
	if err != nil || !got {
		t.Errorf("small skew: got=%v err=%v, want pass", got, err)
	}

	// A day ahead: error wrapping ErrCoercion, not a pass.
	_, err = e.Evaluate(now.Add(24*time.Hour).Format(time.RFC3339), now)
	if err == nil {
		t.Fatal("future timestamp evaluated without error (would silently pass age <)")
	}
	if !errors.Is(err, ErrCoercion) {
		t.Errorf("future-timestamp error does not wrap ErrCoercion: %v", err)
	}

	// age > must error the same way, not silently fail.
	gt, err2 := ParseExpect("age > 1h")
	if err2 != nil {
		t.Fatal(err2)
	}
	if _, err := gt.Evaluate(now.Add(24*time.Hour).Format(time.RFC3339), now); !errors.Is(err, ErrCoercion) {
		t.Errorf("age >: future timestamp err = %v, want ErrCoercion", err)
	}
}

// SQL scalars arrive in several timestamp shapes; age comparisons must coerce each.
func TestEvaluateTimeLayouts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	e, err := ParseExpect("age < 8d")
	if err != nil {
		t.Fatal(err)
	}
	for _, actual := range []string{
		"2026-06-12T12:00:00Z",      // RFC3339, 1 day old
		"2026-06-12 12:00:00",       // space-separated, no zone
		"2026-06-12 12:00:00+00:00", // space-separated, zoned
		"2026-06-12",                // date only
	} {
		got, err := e.Evaluate(actual, now)
		if err != nil {
			t.Errorf("Evaluate(%q): coercion failed: %v", actual, err)
			continue
		}
		if !got {
			t.Errorf("Evaluate(%q) = false, want true (1 day old < 8d)", actual)
		}
	}
}
