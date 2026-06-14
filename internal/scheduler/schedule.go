package scheduler

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Schedule's Next is pure in its input time, so it stays correct across daemon
// downtime. Build with ParseSchedule.
type Schedule struct {
	spec  string
	sched cron.Schedule
}

// Next returns the first fire strictly after t.
func (s Schedule) Next(t time.Time) time.Time { return s.sched.Next(t) }

func (s Schedule) String() string { return s.spec }

var weekdays = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// shorthandRe is anchored, so a multi-field cron expression falls through to the
// cron parser.
var shorthandRe = regexp.MustCompile(`(?i)^(?:([a-z]{3})\s+)?([0-2]?\d):([0-5]\d)$`)

// ParseSchedule parses shorthand ("Sun 04:10", "04:10") or a 5-field cron
// expression, as UTC unless the expression carries its own CRON_TZ=/TZ= prefix.
func ParseSchedule(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Schedule{}, fmt.Errorf("empty schedule")
	}
	cronSpec, err := shorthandToCron(spec)
	if err != nil {
		return Schedule{}, err
	}
	sched, err := cron.ParseStandard(withUTC(cronSpec))
	if err != nil {
		return Schedule{}, fmt.Errorf("invalid schedule %q: %w", spec, err)
	}
	return Schedule{spec: spec, sched: sched}, nil
}

// shorthandToCron returns non-shorthand unchanged, for the cron parser to validate.
func shorthandToCron(spec string) (string, error) {
	m := shorthandRe.FindStringSubmatch(spec)
	if m == nil {
		return spec, nil
	}
	hour, _ := strconv.Atoi(m[2]) // regex guarantees digits
	minute, _ := strconv.Atoi(m[3])
	if hour > 23 {
		return "", fmt.Errorf("invalid schedule %q: hour %d out of range (0-23)", spec, hour)
	}
	dow := "*"
	if m[1] != "" {
		d, ok := weekdays[strings.ToLower(m[1])]
		if !ok {
			return "", fmt.Errorf("invalid schedule %q: unknown weekday %q", spec, m[1])
		}
		dow = strconv.Itoa(d)
	}
	return fmt.Sprintf("%d %d * * %s", minute, hour, dow), nil
}

// withUTC leaves descriptors (@weekly) and user-supplied TZ prefixes alone.
func withUTC(cronSpec string) string {
	if strings.HasPrefix(cronSpec, "@") ||
		strings.HasPrefix(cronSpec, "CRON_TZ=") ||
		strings.HasPrefix(cronSpec, "TZ=") {
		return cronSpec
	}
	return "CRON_TZ=UTC " + cronSpec
}
