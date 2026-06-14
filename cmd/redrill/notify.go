package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/alyamovsky/redrill/internal/checks"
	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/exec"
	"github.com/alyamovsky/redrill/internal/notify"
	"github.com/alyamovsky/redrill/internal/orchestrate"
	"github.com/alyamovsky/redrill/internal/scheduler"
	"github.com/alyamovsky/redrill/internal/store"
)

// ioPolicy maps the config's IO-discipline knobs to the executor's policy.
func ioPolicy(cfg *config.Config) exec.IOPolicy {
	return exec.IOPolicy{
		NiceCPU:      cfg.Nice.CPU,
		IOClass:      cfg.Nice.IOClass,
		BandwidthKiB: cfg.BandwidthLimit.Bytes() / 1024,
	}
}

const staleSweepInterval = time.Hour

// alerter turns finished runs and proof staleness into notifications. It holds
// the dedup state that keeps one stale episode from re-alerting every sweep.
type alerter struct {
	n      *notify.Notifier
	st     *store.Store
	drills map[string]config.Drill
	now    func() time.Time

	mu    sync.Mutex
	stale map[string]bool // drills currently in a stale-alerted state
}

func newAlerter(n *notify.Notifier, st *store.Store, drills []config.Drill, now func() time.Time) *alerter {
	m := make(map[string]config.Drill, len(drills))
	for _, d := range drills {
		m[d.Name] = d
	}
	return &alerter{n: n, st: st, drills: m, now: now, stale: map[string]bool{}}
}

func (a *alerter) active() bool { return a != nil && a.n != nil }

// afterRun emits fail/error/recover for a just-finished run.
func (a *alerter) afterRun(ctx context.Context, d config.Drill, res orchestrate.RunResult) {
	if !a.active() {
		return
	}
	a.mu.Lock()
	wasStale := a.stale[d.Name]
	a.mu.Unlock()

	ev, ok := notify.ClassifyRun(a.prevResult(ctx, d.Name, res.RunID), string(res.Status), wasStale)
	if !ok {
		return
	}
	if res.Status == store.ResultPass {
		a.mu.Lock()
		delete(a.stale, d.Name)
		a.mu.Unlock()
	}
	note := notify.Notification{
		Event:      ev,
		Drill:      d.Name,
		Level:      res.LevelReached,
		LastProven: a.lastProven(ctx, d),
		Now:        a.now(),
	}
	if ev == notify.EventFail || ev == notify.EventError {
		if lvl, detail := diagnose(res); detail != "" {
			note.Level, note.Detail = lvl, detail
		}
	}
	a.n.Dispatch(ctx, note)
}

// sweepStale fires stale for drills that crossed their Proof SLA and clears the
// flag for those that recovered — so a stale episode alerts once, not every tick.
func (a *alerter) sweepStale(ctx context.Context) {
	if !a.active() {
		return
	}
	now := a.now()
	for name, d := range a.drills {
		level := scheduler.HeadlineLevel(d)
		proven := a.proof(ctx, name, level)
		stale := scheduler.Stale(d.MaxProofAge.Duration(), proven, now)

		a.mu.Lock()
		already := a.stale[name]
		switch {
		case stale && !already:
			a.stale[name] = true
		case !stale && already:
			delete(a.stale, name)
		}
		a.mu.Unlock()

		if stale && !already {
			a.n.Dispatch(ctx, notify.Notification{
				Event:       notify.EventStale,
				Drill:       name,
				Level:       level,
				LastProven:  proven,
				MaxProofAge: d.MaxProofAge.Duration(),
				Now:         now,
			})
		}
	}
}

// runSweeps sweeps once at startup (catching drills that went stale while the
// daemon was down) then on a ticker until ctx is canceled.
func (a *alerter) runSweeps(ctx context.Context) {
	if !a.active() {
		return
	}
	a.sweepStale(ctx)
	t := time.NewTicker(staleSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepStale(ctx)
		}
	}
}

// prevResult returns the result of the run before runID, "" if none.
func (a *alerter) prevResult(ctx context.Context, drill string, runID int64) string {
	runs, err := a.st.ListRuns(ctx, drill, 2)
	if err != nil {
		return ""
	}
	for _, r := range runs {
		if r.ID < runID && r.Result != "" {
			return string(r.Result)
		}
	}
	return ""
}

func (a *alerter) lastProven(ctx context.Context, d config.Drill) time.Time {
	return a.proof(ctx, d.Name, scheduler.HeadlineLevel(d))
}

func (a *alerter) proof(ctx context.Context, drill, level string) time.Time {
	if level == "" {
		return time.Time{}
	}
	at, ok, err := a.st.GetProof(ctx, drill, level)
	if err != nil || !ok {
		return time.Time{}
	}
	return at
}

// diagnose extracts the level and a one-line cause from the first failing or
// erroring level, for the message's diagnosis-first line.
func diagnose(res orchestrate.RunResult) (level, detail string) {
	for _, lv := range res.Levels {
		if lv.Status != string(checks.Fail) && lv.Status != string(checks.Error) {
			continue
		}
		for _, ev := range lv.Evidence {
			if ev.Status == checks.Fail || ev.Status == checks.Error {
				return lv.Level, formatEvidence(ev)
			}
		}
		return lv.Level, lv.Summary // executor-level failure with no per-check evidence
	}
	return "", ""
}

func formatEvidence(ev checks.Evidence) string {
	switch {
	case ev.Target != "" && ev.Expected != "":
		return fmt.Sprintf("%s %s: expected %s, got %s", ev.Kind, ev.Target, ev.Expected, ev.Actual)
	case ev.Expected != "":
		return fmt.Sprintf("%s: expected %s, got %s", ev.Kind, ev.Expected, ev.Actual)
	default:
		return fmt.Sprintf("%s: %s", ev.Kind, ev.Actual)
	}
}

// extraValidate runs the checks config can't reach: schedule grammar and notify
// URL parseability.
func extraValidate(cfg *config.Config) error {
	return errors.Join(checkSchedules(cfg), checkNotify(cfg))
}

func checkNotify(cfg *config.Config) error {
	if err := notify.Validate(cfg.Notify.URLs); err != nil {
		return fmt.Errorf("notify.urls: %w", err)
	}
	return nil
}
