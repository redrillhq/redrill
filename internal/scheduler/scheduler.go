package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
)

// Clock is the scheduler's view of time: Now drives scheduling decisions
// (injected so tests are deterministic) and After waits for the next fire.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now().UTC() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RunFunc executes one drill to completion. serve wires it to the orchestrator;
// tests inject a fake. The scheduler owns the timeout and single-flight gating
// around each call.
type RunFunc func(ctx context.Context, drill config.Drill) error

// job pairs a drill with its parsed schedule and its next fire time. base is the
// next un-jittered scheduled instant; fire is base plus this period's jitter and
// is what the loop actually waits for.
type job struct {
	drill    config.Drill
	schedule Schedule
	base     time.Time
	fire     time.Time
}

// Options configure a Scheduler. The zero value is valid: Concurrency defaults
// to 1, and Clock/Logger/Rng default to real implementations.
type Options struct {
	Concurrency int
	Clock       Clock
	Logger      *slog.Logger
	Rng         func() float64 // jitter fraction in [0,1); injectable for tests
}

// Scheduler fires drills on their schedules with jitter, global single-flight,
// and per-run timeouts. Time-dependent decisions use an injected Clock; the
// runs themselves go through RunFunc. See DESIGN.md §9.6.
type Scheduler struct {
	clock Clock
	run   RunFunc
	log   *slog.Logger
	rng   func() float64
	sem   chan struct{} // global single-flight token bucket, cap = concurrency
	jobs  []*job
	wg    sync.WaitGroup // tracks in-flight runs for graceful shutdown
}

// New builds a Scheduler over drills, parsing each schedule up front (an invalid
// schedule is a configuration error). run is the per-drill executor.
func New(drills []config.Drill, run RunFunc, opts Options) (*Scheduler, error) {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Rng == nil {
		opts.Rng = rand.Float64
	}
	s := &Scheduler{
		clock: opts.Clock,
		run:   run,
		log:   opts.Logger,
		rng:   opts.Rng,
		sem:   make(chan struct{}, opts.Concurrency),
	}
	now := s.clock.Now()
	for i := range drills {
		sched, err := ParseSchedule(drills[i].Schedule)
		if err != nil {
			return nil, fmt.Errorf("drill %s: %w", drills[i].Name, err)
		}
		j := &job{drill: drills[i], schedule: sched}
		s.advance(j, now)
		s.jobs = append(s.jobs, j)
	}
	return s, nil
}

// Run drives the scheduler loop until ctx is canceled, then waits for in-flight
// runs to wind down (their contexts derive from ctx, so they are already
// canceling). It returns ctx.Err().
func (s *Scheduler) Run(ctx context.Context) error {
	if len(s.jobs) == 0 {
		s.log.Warn("no drills scheduled; serve is idle")
	}
	for {
		now := s.clock.Now()
		due, next := s.due(now)
		for _, j := range due {
			s.dispatch(ctx, j)
		}

		var wake <-chan time.Time
		if !next.IsZero() {
			wait := next.Sub(now)
			if wait < 0 {
				wait = 0
			}
			wake = s.clock.After(wait)
		}
		// A nil wake channel blocks forever in select, so with no jobs the loop
		// simply waits for cancellation.
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopping; waiting for in-flight runs")
			s.wg.Wait()
			return ctx.Err()
		case <-wake:
		}
	}
}

// due returns the jobs due at now, advancing each to its next fire, plus the
// soonest upcoming fire across all jobs (zero if there are none). It is pure
// given now — the caller dispatches — so it is deterministic to test. Missed
// fires are not replayed: a job advances to the next slot strictly after now, so
// downtime never produces a backlog burst (staleness covers the gap instead).
func (s *Scheduler) due(now time.Time) ([]*job, time.Time) {
	var ready []*job
	var soonest time.Time
	for _, j := range s.jobs {
		if !j.fire.After(now) {
			ready = append(ready, j)
			s.advance(j, now)
		}
		if soonest.IsZero() || j.fire.Before(soonest) {
			soonest = j.fire
		}
	}
	return ready, soonest
}

// advance sets a job's next fire to the first scheduled slot after now, plus a
// fresh jitter offset for this period.
func (s *Scheduler) advance(j *job, now time.Time) {
	j.base = j.schedule.Next(now)
	j.fire = j.base.Add(s.jitter(j.drill.Jitter.Duration()))
}

// jitter returns a random delay in [0, max). max <= 0 yields no jitter.
func (s *Scheduler) jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(s.rng() * float64(max))
}

// dispatch runs a drill if a single-flight slot is free, else skips it with a
// warning (single-flight drops excess rather than queueing heavy restores — the
// next scheduled run will try again). The run is bounded by the drill's timeout.
func (s *Scheduler) dispatch(ctx context.Context, j *job) {
	select {
	case s.sem <- struct{}{}:
	default:
		s.log.Warn("drill skipped: another run is in flight (single-flight)", "drill", j.drill.Name)
		return
	}
	s.wg.Add(1)
	go func(d config.Drill) {
		defer s.wg.Done()
		defer func() { <-s.sem }()

		rctx := ctx
		if to := d.Timeout.Duration(); to > 0 {
			var cancel context.CancelFunc
			rctx, cancel = context.WithTimeout(ctx, to)
			defer cancel()
		}
		s.log.Info("drill started", "drill", d.Name)
		if err := s.run(rctx, d); err != nil {
			s.log.Error("drill run failed", "drill", d.Name, "error", err)
		}
	}(j.drill)
}
