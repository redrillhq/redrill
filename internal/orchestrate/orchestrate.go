// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/redrillhq/redrill/internal/checks"
	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/exec"
	"github.com/redrillhq/redrill/internal/store"
)

const statusSkipped = "skipped"

// Orchestrator drives one run state machine per drill and owns all
// run/step/evidence writing.
type Orchestrator struct {
	store *store.Store
	exec  exec.Executor
	now   func() time.Time
	host  string
	log   *slog.Logger
}

// now is the injected clock (UTC).
func New(st *store.Store, ex exec.Executor, now func() time.Time) *Orchestrator {
	return &Orchestrator{
		store: st, exec: ex, now: now, host: ex.Describe().Host,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// WithLogger sets the logger used for non-fatal housekeeping (e.g. retention).
func (o *Orchestrator) WithLogger(l *slog.Logger) *Orchestrator {
	if l != nil {
		o.log = l
	}
	return o
}

type RunOptions struct {
	Trigger store.Trigger      // default manual
	Level   string             // "" runs all configured levels in order; else only this one
	Report  func(LevelOutcome) // optional, called per level
	Scratch config.Scratch
	// Keep leaves the L3 sandbox and scratch restore in place for forensics
	// (`run --keep`, CLI-only); the next serve start reaps them.
	Keep bool
}

// LevelOutcome is one level's result, for streaming and rendering.
type LevelOutcome struct {
	Level    string
	Status   string // pass | fail | error | skipped
	Summary  string
	Evidence []checks.Evidence
}

type RunResult struct {
	RunID        int64
	Status       store.Result // pass | fail | error
	LevelReached string
	Levels       []LevelOutcome
	KeptSandbox  string // container id left running under RunOptions.Keep
}

// levelState accumulates the cross-level run state: the snapshot pin (one
// snapshot per run), restored totals, and any sandbox kept for forensics.
type levelState struct {
	pin   string
	pinAt time.Time
	kept  string
	bytes int64
	files int
}

type leveled struct {
	name string
	on   bool
}

// Run executes drill against src, writing its steps and evidence and advancing
// drill_state on a full pass. A returned error means the run couldn't be carried
// out at all; a completed run returns its verdict in RunResult.Status.
func (o *Orchestrator) Run(ctx context.Context, drill config.Drill, src config.Source, opts RunOptions) (RunResult, error) {
	levels, err := selectLevels(drill, opts.Level)
	if err != nil {
		return RunResult{}, err
	}

	trigger := opts.Trigger
	if trigger == "" {
		trigger = store.TriggerManual
	}
	start := o.now().UTC()
	runID, err := o.store.CreateRun(ctx, store.Run{Drill: drill.Name, Trigger: trigger, StartedAt: start, Executor: o.host})
	if err != nil {
		return RunResult{}, fmt.Errorf("create run for %s: %w", drill.Name, err)
	}
	result := RunResult{RunID: runID}

	// Finalize even on the error path so a mid-run store failure or a canceled
	// ctx never leaves a zombie run (result NULL). WithoutCancel lets cleanup
	// persist after a timeout cancellation.
	// One snapshot per run: the first level to resolve one pins the rest, so a
	// backup landing mid-run cannot split the audit across snapshots. Recorded
	// on the run row (with the snapshot's own timestamp — the RPO input) so
	// evidence still names the backup it tested after the source rotates.
	var st levelState
	finished := false
	defer func() {
		if finished {
			return
		}
		end := o.now().UTC()
		_ = o.store.FinishRun(context.WithoutCancel(ctx), store.Run{
			ID: runID, Result: store.ResultError, LevelReached: result.LevelReached,
			BytesRestored: st.bytes, FilesRestored: int64(st.files),
			DurationMS: end.Sub(start).Milliseconds(), FinishedAt: end,
			Snapshot: st.pin, SnapshotTime: st.pinAt,
		})
	}()

	// file_count_tolerance baseline, read orchestrator-side since checks never touch the store.
	prevFileCount := 0
	if last, ok, err := o.store.LastBaselineRun(ctx, drill.Name); err != nil {
		// A missing baseline degrades the check to its no-baseline pass; say so.
		o.log.Warn("baseline lookup failed", "drill", drill.Name, "error", err.Error())
	} else if ok {
		prevFileCount = int(last.FilesRestored)
	}

	shortCircuit := false
	for _, lv := range levels {
		outcome, ran, err := o.runLevel(ctx, runID, drill, src, lv, start, shortCircuit, opts, prevFileCount, &st)
		if err != nil {
			return RunResult{}, err
		}
		if ran {
			result.LevelReached = lv.name
			if outcome.Status == string(checks.Fail) || outcome.Status == string(checks.Error) {
				shortCircuit = true
			}
		}
		result.Levels = append(result.Levels, outcome)
		if opts.Report != nil {
			opts.Report(outcome)
		}
	}

	result.Status = aggregateRun(result.Levels)
	result.KeptSandbox = st.kept
	end := o.now().UTC()
	fin := store.Run{
		ID:            runID,
		Result:        result.Status,
		LevelReached:  result.LevelReached,
		BytesRestored: st.bytes,
		FilesRestored: int64(st.files),
		DurationMS:    end.Sub(start).Milliseconds(),
		FinishedAt:    end,
		Snapshot:      st.pin,
		SnapshotTime:  st.pinAt,
	}
	// WithoutCancel so a run whose work completed keeps its real verdict even if
	// ctx expired at the wire; the defer above is only the abnormal-path backstop.
	if err := o.store.FinishRun(context.WithoutCancel(ctx), fin); err != nil {
		return RunResult{}, fmt.Errorf("finish run %d: %w", runID, err)
	}
	finished = true
	// Best-effort housekeeping: the monotonic metrics counter (never changes a
	// run's verdict).
	if err := o.store.AddBytesRestored(context.WithoutCancel(ctx), drill.Name, st.bytes); err != nil {
		o.log.Warn("bytes counter update failed", "drill", drill.Name, "error", err.Error())
	}
	// Proofs advance only when the whole run passes (DESIGN §9.8) — a level that
	// passed inside a failed or errored run advances nothing.
	if result.Status == store.ResultPass {
		for _, lv := range result.Levels {
			if lv.Status != string(checks.Pass) {
				continue
			}
			if err := o.store.RecordProof(context.WithoutCancel(ctx), drill.Name, lv.Level, start); err != nil {
				return result, fmt.Errorf("record proof for %s/%s: %w", drill.Name, lv.Level, err)
			}
		}
	}
	o.pruneRetention(ctx, drill, end)
	return result, nil
}

// pruneRetention enforces the drill's age+count retention; failures are
// non-fatal housekeeping and never change a run's verdict.
func (o *Orchestrator) pruneRetention(ctx context.Context, drill config.Drill, now time.Time) {
	maxAge := drill.Retention.MaxAge.Duration()
	maxCount := drill.Retention.MaxCount
	if maxAge <= 0 && maxCount <= 0 {
		return
	}
	if n, err := o.store.Prune(context.WithoutCancel(ctx), drill.Name, maxAge, maxCount, now); err != nil {
		o.log.Warn("retention prune failed", "drill", drill.Name, "error", err.Error())
	} else if n > 0 {
		o.log.Info("pruned old runs", "drill", drill.Name, "count", n)
	}
}

// ran reports whether the level actually executed (vs. skipped).
func (o *Orchestrator) runLevel(ctx context.Context, runID int64, drill config.Drill, src config.Source, lv leveled, start time.Time, shortCircuit bool, opts RunOptions, prevFileCount int, st *levelState) (LevelOutcome, bool, error) {
	// start is the run's logical clock (check evaluation, proof time); stepStart
	// is this level's own start so per-level durations are real.
	stepStart := o.now().UTC()
	if shortCircuit {
		out := LevelOutcome{Level: lv.name, Status: statusSkipped, Summary: "skipped (a lower level did not pass)"}
		return out, false, o.recordStep(ctx, runID, out, stepStart)
	}

	res, err := o.exec.RunStep(ctx, o.buildStep(runID, drill, src, lv, start, opts, prevFileCount, st.pin))
	switch {
	case errors.Is(err, exec.ErrUnsupported):
		out := LevelOutcome{Level: lv.name, Status: statusSkipped, Summary: "skipped (unsupported level/source combination)"}
		return out, false, o.recordStep(ctx, runID, out, stepStart)
	case errors.Is(err, exec.ErrNoSandboxRuntime):
		// Degrades to skipped, never a silent pass.
		out := LevelOutcome{Level: lv.name, Status: statusSkipped, Summary: "skipped (no sandbox runtime)"}
		return out, false, o.recordStep(ctx, runID, out, stepStart)
	case err != nil:
		out := LevelOutcome{Level: lv.name, Status: string(checks.Error), Summary: "executor: " + err.Error()}
		return out, true, o.recordStep(ctx, runID, out, stepStart)
	}

	if st.pin == "" && res.Snapshot != "" {
		st.pin, st.pinAt = res.Snapshot, res.SnapshotTime
	}
	if res.KeptSandbox != "" {
		st.kept = res.KeptSandbox
	}
	out := LevelOutcome{Level: lv.name, Status: string(res.Status), Summary: res.Summary, Evidence: res.Evidence}
	for _, ev := range res.Evidence {
		row := store.Evidence{
			RunID: runID, CheckKind: ev.Kind, Target: ev.Target,
			Expected: ev.Expected, Actual: ev.Actual, Status: string(ev.Status), Weak: ev.Weak,
		}
		if err := o.store.AddEvidence(ctx, row); err != nil {
			return out, true, fmt.Errorf("write evidence for run %d: %w", runID, err)
		}
	}
	if err := o.recordStep(ctx, runID, out, stepStart); err != nil {
		return out, true, err
	}
	st.bytes += res.Bytes
	st.files += res.Files
	return out, true, nil
}

func (o *Orchestrator) recordStep(ctx context.Context, runID int64, out LevelOutcome, start time.Time) error {
	step := store.RunStep{
		RunID: runID, Kind: out.Level, StartedAt: start, FinishedAt: o.now().UTC(),
		Status: out.Status, Summary: out.Summary,
	}
	if err := o.store.AddStep(ctx, step); err != nil {
		return fmt.Errorf("write step %s for run %d: %w", out.Level, runID, err)
	}
	return nil
}

func (o *Orchestrator) buildStep(runID int64, drill config.Drill, src config.Source, lv leveled, now time.Time, opts RunOptions, prevFileCount int, pin string) exec.StepSpec {
	spec := exec.StepSpec{
		RunID: runID, Drill: drill.Name, Level: lv.name, Source: src, Now: now,
		Scratch: opts.Scratch, PrevFileCount: prevFileCount, Snapshot: pin, Keep: opts.Keep,
	}
	switch lv.name {
	case "l1":
		spec.L1 = drill.Levels.L1
	case "l2":
		spec.L2 = drill.Levels.L2
	case "l3":
		spec.L3 = drill.Levels.L3
	}
	return spec
}

// selectLevels returns configured levels ascending, optionally filtered to one.
// Asking for an unconfigured level is a usage error.
func selectLevels(drill config.Drill, only string) ([]leveled, error) {
	all := []leveled{
		{"l1", drill.Levels.L1 != nil},
		{"l2", drill.Levels.L2 != nil},
		{"l3", drill.Levels.L3 != nil},
	}
	var out []leveled
	for _, lv := range all {
		if !lv.on {
			continue
		}
		if only != "" && only != lv.name {
			continue
		}
		out = append(out, lv)
	}
	if only != "" && len(out) == 0 {
		return nil, fmt.Errorf("drill %s does not configure level %s", drill.Name, only)
	}
	return out, nil
}

// aggregateRun folds level outcomes into the run verdict: fail dominates error
// dominates pass; nothing executed is an error (the auditor proved nothing).
func aggregateRun(levels []LevelOutcome) store.Result {
	ran, fail, errd := false, false, false
	for _, lv := range levels {
		switch lv.Status {
		case string(checks.Pass):
			ran = true
		case string(checks.Fail):
			ran, fail = true, true
		case string(checks.Error):
			ran, errd = true, true
		}
	}
	switch {
	case !ran:
		return store.ResultError
	case fail:
		return store.ResultFail
	case errd:
		return store.ResultError
	default:
		return store.ResultPass
	}
}
