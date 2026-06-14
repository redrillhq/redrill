package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testScheduler(run RunFunc, concurrency int) *Scheduler {
	return &Scheduler{
		clock: realClock{},
		run:   run,
		log:   discardLogger(),
		rng:   func() float64 { return 0 },
		sem:   make(chan struct{}, concurrency),
	}
}

func TestJitterBounds(t *testing.T) {
	t.Parallel()
	const max = 20 * time.Minute
	for _, frac := range []float64{0, 0.25, 0.5, 0.9999} {
		s := &Scheduler{rng: func() float64 { return frac }}
		if d := s.jitter(max); d < 0 || d >= max {
			t.Errorf("jitter(frac=%v) = %v, want within [0, %v)", frac, d, max)
		}
	}
	s := &Scheduler{rng: func() float64 { return 0.9 }}
	if d := s.jitter(0); d != 0 {
		t.Errorf("jitter(0) = %v, want 0 (no jitter configured)", d)
	}
	if d := s.jitter(-time.Second); d != 0 {
		t.Errorf("jitter(negative) = %v, want 0", d)
	}
}

func TestDue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.June, 14, 12, 0, 0, 0, time.UTC)
	daily, err := ParseSchedule("00:00")
	if err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{rng: func() float64 { return 0 }}
	jDue := &job{drill: config.Drill{Name: "due"}, schedule: daily, base: now.Add(-time.Hour), fire: now.Add(-time.Hour)}
	jFuture := &job{drill: config.Drill{Name: "future"}, schedule: daily, base: now.Add(time.Hour), fire: now.Add(time.Hour)}
	s.jobs = []*job{jDue, jFuture}

	due, next := s.due(now)
	if len(due) != 1 || due[0].drill.Name != "due" {
		t.Fatalf("due jobs = %d, want exactly [due]", len(due))
	}
	if !jDue.fire.After(now) {
		t.Errorf("due job not advanced past now: fire=%v", jDue.fire)
	}
	// jDue's next slot is 12h away; the future job (1h) is sooner.
	if !next.Equal(jFuture.fire) {
		t.Errorf("soonest next = %v, want the future job at %v", next, jFuture.fire)
	}
}

func TestDueNoJobs(t *testing.T) {
	t.Parallel()
	s := &Scheduler{}
	due, next := s.due(time.Now())
	if len(due) != 0 || !next.IsZero() {
		t.Errorf("due()=%d jobs, next=%v; want 0 jobs and zero next", len(due), next)
	}
}

func TestDispatchSingleFlight(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32
	run := func(_ context.Context, _ config.Drill) error {
		runs.Add(1)
		started <- struct{}{}
		<-release
		return nil
	}
	s := testScheduler(run, 1)
	ctx := context.Background()

	s.dispatch(ctx, &job{drill: config.Drill{Name: "a"}})
	<-started // a holds the only slot
	s.dispatch(ctx, &job{drill: config.Drill{Name: "b"}})
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d with one in flight; B must be skipped (single-flight)", got)
	}
	close(release)
	s.wg.Wait()
	if got := runs.Load(); got != 1 {
		t.Fatalf("total runs = %d, want 1 (B dropped, not queued)", got)
	}
}

func TestDispatchTimeout(t *testing.T) {
	t.Parallel()
	done := make(chan error, 1)
	run := func(ctx context.Context, _ config.Drill) error {
		<-ctx.Done()
		done <- ctx.Err()
		return ctx.Err()
	}
	s := testScheduler(run, 1)
	j := &job{drill: config.Drill{Name: "slow", Timeout: config.Duration(10 * time.Millisecond)}}
	s.dispatch(context.Background(), j)

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("run ctx error = %v, want DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run never observed its timeout")
	}
	s.wg.Wait()
}

func TestNewBadSchedule(t *testing.T) {
	t.Parallel()
	if _, err := New([]config.Drill{{Name: "d", Schedule: "nonsense"}}, nil, Options{}); err == nil {
		t.Fatal("New with an invalid schedule should error")
	}
}

func TestRunDispatchesThenStops(t *testing.T) {
	t.Parallel()
	ran := make(chan string, 4)
	run := func(_ context.Context, d config.Drill) error { ran <- d.Name; return nil }
	s, err := New([]config.Drill{{Name: "d", Schedule: "Sun 04:10"}}, run, Options{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	// Force the job due so the first loop tick fires it.
	s.jobs[0].base = s.clock.Now().Add(-time.Minute)
	s.jobs[0].fire = s.jobs[0].base

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(ctx) }()

	select {
	case name := <-ran:
		if name != "d" {
			t.Errorf("ran %q, want d", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not run the due drill")
	}
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunIdleNoJobs(t *testing.T) {
	t.Parallel()
	s, err := New(nil, nil, Options{Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(ctx) }()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("idle Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle Run did not return on cancel")
	}
}
