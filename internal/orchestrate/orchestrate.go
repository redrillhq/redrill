package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alyamovsky/redrill/internal/checks"
	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/exec"
	"github.com/alyamovsky/redrill/internal/store"
)

const statusSkipped = "skipped"

// Orchestrator drives one run state machine per drill and owns all
// run/step/evidence writing.
type Orchestrator struct {
	store *store.Store
	exec  exec.Executor
	now   func() time.Time
	host  string
}

// now is the injected clock (UTC).
func New(st *store.Store, ex exec.Executor, now func() time.Time) *Orchestrator {
	return &Orchestrator{store: st, exec: ex, now: now, host: ex.Describe().Host}
}

type RunOptions struct {
	Trigger store.Trigger      // default manual
	Level   string             // "" runs all configured levels in order; else only this one
	Report  func(LevelOutcome) // optional, called per level
	Scratch config.Scratch
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

	// file_count_tolerance baseline, read orchestrator-side since checks never touch the store.
	prevFileCount := 0
	if last, ok, err := o.store.LastRunWithResult(ctx, drill.Name, store.ResultPass); err == nil && ok {
		prevFileCount = int(last.FilesRestored)
	}

	shortCircuit := false
	var bytesRestored int64
	var filesRestored int
	for _, lv := range levels {
		outcome, ran, err := o.runLevel(ctx, runID, drill, src, lv, start, shortCircuit, opts.Scratch, prevFileCount, &bytesRestored, &filesRestored)
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
	end := o.now().UTC()
	fin := store.Run{
		ID:            runID,
		Result:        result.Status,
		LevelReached:  result.LevelReached,
		BytesRestored: bytesRestored,
		FilesRestored: int64(filesRestored),
		DurationMS:    end.Sub(start).Milliseconds(),
		FinishedAt:    end,
	}
	if err := o.store.FinishRun(ctx, fin); err != nil {
		return RunResult{}, fmt.Errorf("finish run %d: %w", runID, err)
	}
	return result, nil
}

// ran reports whether the level actually executed (vs. skipped).
func (o *Orchestrator) runLevel(ctx context.Context, runID int64, drill config.Drill, src config.Source, lv leveled, start time.Time, shortCircuit bool, scratch config.Scratch, prevFileCount int, bytes *int64, files *int) (LevelOutcome, bool, error) {
	if shortCircuit {
		out := LevelOutcome{Level: lv.name, Status: statusSkipped, Summary: "skipped (a lower level did not pass)"}
		return out, false, o.recordStep(ctx, runID, out, start)
	}

	res, err := o.exec.RunStep(ctx, o.buildStep(runID, drill, src, lv, start, scratch, prevFileCount))
	switch {
	case errors.Is(err, exec.ErrUnsupported):
		out := LevelOutcome{Level: lv.name, Status: statusSkipped, Summary: "skipped (level not implemented yet)"}
		return out, false, o.recordStep(ctx, runID, out, start)
	case errors.Is(err, exec.ErrNoSandboxRuntime):
		// Degrades to skipped, never a silent pass.
		out := LevelOutcome{Level: lv.name, Status: statusSkipped, Summary: "skipped (no sandbox runtime)"}
		return out, false, o.recordStep(ctx, runID, out, start)
	case err != nil:
		out := LevelOutcome{Level: lv.name, Status: string(checks.Error), Summary: "executor: " + err.Error()}
		return out, true, o.recordStep(ctx, runID, out, start)
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
	if err := o.recordStep(ctx, runID, out, start); err != nil {
		return out, true, err
	}
	*bytes += res.Bytes
	*files += res.Files
	if res.Status == checks.Pass {
		if err := o.store.RecordProof(ctx, drill.Name, lv.name, start); err != nil {
			return out, true, fmt.Errorf("record proof for %s/%s: %w", drill.Name, lv.name, err)
		}
	}
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

func (o *Orchestrator) buildStep(runID int64, drill config.Drill, src config.Source, lv leveled, now time.Time, scratch config.Scratch, prevFileCount int) exec.StepSpec {
	spec := exec.StepSpec{
		RunID: runID, Drill: drill.Name, Level: lv.name, Source: src, Now: now,
		Scratch: scratch, PrevFileCount: prevFileCount,
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
