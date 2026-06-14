package checks

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The caller maps this to Error, not Fail.
var ErrCoercion = errors.New("cannot coerce actual value")

type expectOp int

const (
	opGT expectOp = iota
	opGE
	opEQ
	opNE
	opBetween
	opAgeLT
	opAgeGT
	opMatches
	opNonempty
)

// Build with ParseExpect. between is inclusive; nonempty treats whitespace-only as empty.
type Expectation struct {
	op  expectOp
	raw string
	a   float64        // operand for > >= == != , and low bound for between
	b   float64        // high bound for between
	dur time.Duration  // operand for age < / age >
	re  *regexp.Regexp // operand for matches
}

func (e Expectation) String() string { return e.raw }

// Operand types are validated here; only coercion of the actual value can fail
// later, in Evaluate.
func ParseExpect(s string) (Expectation, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Expectation{}, fmt.Errorf("expect: empty predicate")
	}
	fields := strings.Fields(raw)
	e := Expectation{raw: raw}

	switch fields[0] {
	case "nonempty":
		if len(fields) != 1 {
			return Expectation{}, fmt.Errorf("expect %q: nonempty takes no operand", raw)
		}
		e.op = opNonempty
		return e, nil

	case "matches":
		if len(fields) < 2 {
			return Expectation{}, fmt.Errorf("expect %q: matches requires a regex", raw)
		}
		pat := strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
		re, err := regexp.Compile(pat)
		if err != nil {
			return Expectation{}, fmt.Errorf("expect %q: invalid regex: %w", raw, err)
		}
		e.op, e.re = opMatches, re
		return e, nil

	case "between":
		a, b, err := twoNumbers(fields)
		if err != nil {
			return Expectation{}, fmt.Errorf("expect %q: between requires two numbers", raw)
		}
		e.op, e.a, e.b = opBetween, a, b
		return e, nil

	case "age":
		if len(fields) != 3 || (fields[1] != "<" && fields[1] != ">") {
			return Expectation{}, fmt.Errorf("expect %q: want `age < DURATION` or `age > DURATION`", raw)
		}
		d, err := parseDuration(fields[2])
		if err != nil {
			return Expectation{}, fmt.Errorf("expect %q: %w", raw, err)
		}
		e.dur = d
		if fields[1] == "<" {
			e.op = opAgeLT
		} else {
			e.op = opAgeGT
		}
		return e, nil
	}

	// Binary numeric comparison: OP N
	if len(fields) != 2 {
		return Expectation{}, fmt.Errorf("expect %q: unrecognized predicate", raw)
	}
	switch fields[0] {
	case ">":
		e.op = opGT
	case ">=":
		e.op = opGE
	case "==":
		e.op = opEQ
	case "!=":
		e.op = opNE
	default:
		return Expectation{}, fmt.Errorf("expect %q: unknown operator %q", raw, fields[0])
	}
	n, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return Expectation{}, fmt.Errorf("expect %q: operand %q is not a number", raw, fields[1])
	}
	e.a = n
	return e, nil
}

// now anchors age comparisons. A coercion failure wraps ErrCoercion.
func (e Expectation) Evaluate(actual string, now time.Time) (bool, error) {
	switch e.op {
	case opNonempty:
		return strings.TrimSpace(actual) != "", nil
	case opMatches:
		return e.re.MatchString(actual), nil
	case opGT, opGE, opEQ, opNE, opBetween:
		n, err := toNumber(actual)
		if err != nil {
			return false, err
		}
		return e.compareNumber(n), nil
	case opAgeLT, opAgeGT:
		t, err := toTime(actual)
		if err != nil {
			return false, err
		}
		age := now.Sub(t)
		if e.op == opAgeLT {
			return age < e.dur, nil
		}
		return age > e.dur, nil
	}
	return false, fmt.Errorf("expect %q: %w", e.raw, ErrCoercion) // unreachable for parsed expectations
}

func (e Expectation) compareNumber(n float64) bool {
	switch e.op {
	case opGT:
		return n > e.a
	case opGE:
		return n >= e.a
	case opEQ:
		return n == e.a
	case opNE:
		return n != e.a
	default: // opBetween
		return n >= e.a && n <= e.b
	}
}

func twoNumbers(fields []string) (float64, float64, error) {
	if len(fields) != 3 {
		return 0, 0, fmt.Errorf("want two numbers")
	}
	a, err1 := strconv.ParseFloat(fields[1], 64)
	b, err2 := strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("not numbers")
	}
	return a, b, nil
}

func toNumber(s string) (float64, error) {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a number", ErrCoercion, s)
	}
	return n, nil
}

// Timestamp shapes SQL scalars commonly arrive in.
var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func toTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: %q is not a timestamp", ErrCoercion, s)
}

// Go durations plus a day suffix (8d).
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(rest); err == nil {
			if n < 0 {
				return 0, fmt.Errorf("negative duration %q", s)
			}
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (want e.g. 30m, 36h, 8d)", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return d, nil
}
