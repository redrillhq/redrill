package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration accepts Go syntax (30m, 36h) plus a day suffix (8d).
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like %q, %q, %q", "30m", "36h", "8d")
	}
	v, err := parseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if rest, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(rest); err == nil {
			if n < 0 {
				return 0, fmt.Errorf("negative duration %q", s)
			}
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (want e.g. 30m, 36h, 8d)", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return v, nil
}

// Size accepts IEC (1MiB, 50GiB), SI (40MB), or a bare integer of bytes.
type Size int64

func (s Size) Bytes() int64 { return int64(s) }

func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	var str string
	if err := n.Decode(&str); err == nil {
		v, err := parseSize(str)
		if err != nil {
			return err
		}
		*s = Size(v)
		return nil
	}
	var i int64
	if err := n.Decode(&i); err != nil {
		return fmt.Errorf("size must be like %q or a byte count", "50GiB")
	}
	if i < 0 {
		return fmt.Errorf("negative size %d", i)
	}
	*s = Size(i)
	return nil
}

// Longest suffix first so "GiB" matches before "B".
var sizeUnits = []struct {
	suffix string
	mult   float64
}{
	{"PiB", 1 << 50}, {"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
	{"PB", 1e15}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
	{"B", 1},
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	for _, u := range sizeUnits {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid size %q (want e.g. 1MiB, 50GiB)", s)
			}
			return int64(n * u.mult), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q (want e.g. 1MiB, 50GiB)", s)
	}
	return n, nil
}
