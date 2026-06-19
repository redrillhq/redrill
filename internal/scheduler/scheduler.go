// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/redrillhq/redrill/internal/config"
)

// Clock is injected so tests are deterministic.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now().UTC() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RunFunc executes one drill. The scheduler owns the timeout and single-flight
// gating around each call.
type RunFunc func(ctx context.Context, drill config.Drill) error

// job's base is the un-jittered scheduled instant; fire is base plus this
// period's jitter, what the loop waits on.
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
	// Gate, when non-nil, is the single-flight token bucket (cap = concurrency).
	// Supplying it lets out-of-band triggers (the API's Run now) share the same
	// gate, so a manual run and a scheduled run never overlap.
	Gate chan struct{}
	// OnCycle, when set, runs once per scheduler loop iteration after due jobs
	// are dispatched — the seam for the healthchecks dead-man ping. It must not
	// block (the cmd wiring fires the ping asynchronously).
	OnCycle func()
}

type Scheduler struct {
	clock   Clock
	run     RunFunc
	log     *slog.Logger
	rng     func() float64
	sem     chan struct{} // single-flight token bucket, cap = concurrency
	onCycle func()
	jobs    []*job
	wg      sync.WaitGroup // in-flight runs, for graceful shutdown
}

// New parses each schedule up front; an invalid schedule is a configuration error.
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
	sem := opts.Gate
	if sem == nil {
		sem = make(chan struct{}, opts.Concurrency)
	}
	s := &Scheduler{
		clock:   opts.Clock,
		run:     run,
		log:     opts.Logger,
		rng:     opts.Rng,
		sem:     sem,
		onCycle: opts.OnCycle,
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

// Run loops until ctx is canceled, then waits for in-flight runs (whose contexts
// derive from ctx). Returns ctx.Err().
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
		// One heartbeat per cycle: fires at startup and on every wake, so a
		// dead-man monitor (healthchecks) learns the daemon is alive.
		if s.onCycle != nil {
			s.onCycle()
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
		// waits for cancellation.
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopping; waiting for in-flight runs")
			s.wg.Wait()
			return ctx.Err()
		case <-wake:
		}
	}
}

// due returns the jobs due at now (advancing each) plus the soonest upcoming fire
// (zero if none). Missed fires aren't replayed — a job advances to the next slot
// strictly after now, so downtime produces no backlog burst; staleness covers the gap.
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

func (s *Scheduler) advance(j *job, now time.Time) {
	j.base = j.schedule.Next(now)
	j.fire = j.base.Add(s.jitter(j.drill.Jitter.Duration()))
}

// jitter returns a delay in [0, max); max <= 0 yields none.
func (s *Scheduler) jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(s.rng() * float64(max))
}

// dispatch drops a drill when no single-flight slot is free — excess isn't
// queued; the next scheduled run retries.
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
