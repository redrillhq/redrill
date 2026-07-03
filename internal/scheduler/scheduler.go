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

// job's fire is the next scheduled instant plus this period's jitter — what the
// loop waits on.
type job struct {
	drill    config.Drill
	schedule Schedule
	fire     time.Time
}

// Gate is the shared single-flight gate: a global token bucket (cap =
// concurrency) plus a per-drill in-flight set, so concurrency > 1 still never
// runs the same drill twice at once. The scheduler and out-of-band triggers
// (the API's Run now) share one Gate.
type Gate struct {
	sem      chan struct{}
	mu       sync.Mutex
	inflight map[string]bool
}

func NewGate(concurrency int) *Gate {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Gate{sem: make(chan struct{}, concurrency), inflight: map[string]bool{}}
}

// TryAcquire claims a slot for drill; ok=false when every slot is busy or the
// drill is already in flight. The returned release is idempotent.
func (g *Gate) TryAcquire(drill string) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight[drill] {
		return nil, false
	}
	select {
	case g.sem <- struct{}{}:
	default:
		return nil, false
	}
	g.inflight[drill] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.inflight, drill)
			g.mu.Unlock()
			<-g.sem
		})
	}, true
}

// Options configure a Scheduler. The zero value is valid: Concurrency defaults
// to 1, and Clock/Logger/Rng default to real implementations.
type Options struct {
	Concurrency int
	Clock       Clock
	Logger      *slog.Logger
	Rng         func() float64 // jitter fraction in [0,1); injectable for tests
	// Gate, when non-nil, is the shared single-flight gate. Supplying it lets
	// out-of-band triggers (the API's Run now) share the same gate, so a manual
	// run and a scheduled run never overlap.
	Gate *Gate
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
	gate    *Gate
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
	gate := opts.Gate
	if gate == nil {
		gate = NewGate(opts.Concurrency)
	}
	s := &Scheduler{
		clock:   opts.Clock,
		run:     run,
		log:     opts.Logger,
		rng:     opts.Rng,
		gate:    gate,
		onCycle: opts.OnCycle,
	}
	now := s.clock.Now()
	for i := range drills {
		if drills[i].Schedule == "" {
			continue // manual-only drill: never auto-fires (run via CLI/API/hook)
		}
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
	j.fire = j.schedule.Next(now).Add(s.jitter(j.drill.Jitter.Duration()))
}

// jitter returns a delay in [0, max); max <= 0 yields none.
func (s *Scheduler) jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(s.rng() * float64(max))
}

// dispatch drops a drill when no single-flight slot is free (or the same drill
// is already running) — excess isn't queued; the next scheduled run retries.
func (s *Scheduler) dispatch(ctx context.Context, j *job) {
	release, ok := s.gate.TryAcquire(j.drill.Name)
	if !ok {
		s.log.Warn("drill skipped: another run is in flight (single-flight)", "drill", j.drill.Name)
		return
	}
	s.wg.Add(1)
	go func(d config.Drill) {
		defer s.wg.Done()
		defer release()

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
