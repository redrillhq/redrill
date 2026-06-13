package exec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alyamovsky/drillbit/internal/checks"
	"github.com/alyamovsky/drillbit/internal/config"
	"github.com/alyamovsky/drillbit/internal/driver/dumpdir"
	"github.com/alyamovsky/drillbit/internal/redact"
)

// ErrUnsupported is returned (wrapped) when a step's (level, source type) isn't
// implemented yet — the orchestrator records that level as skipped and moves on,
// rather than treating it as a failure. (M5 implements L1 for dumpdir; borg L1
// lands in M6, L2 in M7, L3 in M8.)
var ErrUnsupported = errors.New("unsupported step")

// StepSpec describes one unit of engine/check work to run. It must stay
// serializable — no func fields, channels, or handles — so a future remote agent
// can run it near the data (DESIGN §9.4). Secret-bearing fields travel only as
// the *_file/*_env references inside Source; the executor resolves them locally,
// where the data lives.
type StepSpec struct {
	RunID  int64         `json:"run_id"`
	Drill  string        `json:"drill"`
	Level  string        `json:"level"`
	Source config.Source `json:"source"`
	L1     *config.L1    `json:"l1,omitempty"` // L2/L3 specs join this in M7/M8
	Now    time.Time     `json:"now"`
}

// StepResult is the serializable outcome of a step: the per-check evidence plus
// an aggregate status. Status is pass|fail|error (a level skipped for short-
// circuit is recorded by the orchestrator, not returned here).
type StepResult struct {
	Level    string            `json:"level"`
	Status   checks.Status     `json:"status"`
	Evidence []checks.Evidence `json:"evidence,omitempty"`
	Summary  string            `json:"summary"`
	Files    int               `json:"files"`
	Bytes    int64             `json:"bytes"`
}

// ExecutorInfo describes where and what an executor can run.
type ExecutorInfo struct {
	Host string `json:"host"`
}

// Executor is the multi-host seam (DESIGN §9.2, §9.4). LocalExecutor is the only
// v1 implementation; a Phase 4 agent is an additive transport over the same
// serializable StepSpec/StepResult.
type Executor interface {
	Describe() ExecutorInfo
	RunStep(ctx context.Context, step StepSpec) (StepResult, error)
}

// LocalExecutor runs steps in this process, against the local filesystem and
// engines.
type LocalExecutor struct {
	host string
}

// NewLocal returns a LocalExecutor identifying itself as host.
func NewLocal(host string) *LocalExecutor { return &LocalExecutor{host: host} }

func (e *LocalExecutor) Describe() ExecutorInfo { return ExecutorInfo{Host: e.host} }

// RunStep dispatches by (level, source type). Unimplemented combinations return
// a wrapped ErrUnsupported; every other outcome — including "the backup is bad"
// (fail) and "couldn't check" (error) — is reported in StepResult with a nil
// error.
func (e *LocalExecutor) RunStep(ctx context.Context, step StepSpec) (StepResult, error) {
	if step.Level == "l1" && step.Source.Type == "dumpdir" {
		return runDumpdirL1(ctx, step)
	}
	return StepResult{}, fmt.Errorf("%w: level %q source %q", ErrUnsupported, step.Level, step.Source.Type)
}

func runDumpdirL1(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	// Redaction is the mandatory boundary before any captured text becomes
	// evidence (DESIGN §9.7). dumpdir has no secrets, so this redactor is empty;
	// borg/restic populate it from *_file/*_env in M6+.
	red := redact.New()

	d := dumpdir.New(step.Source.Path, step.Source.Pattern)
	if err := d.Validate(ctx); err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	snaps, err := d.ListSnapshots(ctx)
	if err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	if len(snaps) == 0 {
		return errorStep(res, fmt.Sprintf("no files match %q in %s", step.Source.Pattern, step.Source.Path)), nil
	}

	selected := snaps[:1] // pick: newest (default)
	if step.Source.Pick == "all-matching-window" {
		selected = snaps
	}
	for _, s := range selected {
		for _, c := range l1Checks(step.L1, d.Path(s.ID)) {
			ev, err := c.Run(ctx, checks.CheckEnv{Now: step.Now})
			if err != nil {
				ev = checks.Evidence{Kind: c.Kind(), Target: s.ID, Status: checks.Error, Actual: err.Error()}
			}
			redactEvidence(red, &ev)
			res.Evidence = append(res.Evidence, ev)
		}
	}
	res.Files = len(selected)
	res.Status = aggregate(res.Evidence)
	res.Summary = red.Redact(summarize(res.Status, res.Evidence))
	return res, nil
}

// l1Checks builds the configured L1 dump checks for one file. Each L1 field is a
// pointer so "unset" is distinct from "zero" (config.L1).
func l1Checks(l1 *config.L1, path string) []checks.Check {
	if l1 == nil {
		return nil
	}
	var cs []checks.Check
	if l1.FileMinBytes != nil {
		cs = append(cs, checks.FileMinBytes{Path: path, Min: l1.FileMinBytes.Bytes()})
	}
	if l1.CompressionTest != nil && *l1.CompressionTest {
		cs = append(cs, checks.CompressionTest{Path: path})
	}
	if l1.MaxAge != nil {
		cs = append(cs, checks.MaxAge{Path: path, Max: l1.MaxAge.Duration()})
	}
	return cs
}

// aggregate folds per-check verdicts into a level status. fail dominates error
// (a definitive "backup is bad" outranks "couldn't check one thing"); error
// dominates pass.
func aggregate(evs []checks.Evidence) checks.Status {
	hasFail, hasError := false, false
	for _, ev := range evs {
		switch ev.Status {
		case checks.Fail:
			hasFail = true
		case checks.Error:
			hasError = true
		}
	}
	switch {
	case hasFail:
		return checks.Fail
	case hasError:
		return checks.Error
	default:
		return checks.Pass
	}
}

func summarize(st checks.Status, evs []checks.Evidence) string {
	var pass, fail, errc int
	for _, ev := range evs {
		switch ev.Status {
		case checks.Pass:
			pass++
		case checks.Fail:
			fail++
		case checks.Error:
			errc++
		}
	}
	return fmt.Sprintf("%s: %d checks (%d pass, %d fail, %d error)", st, len(evs), pass, fail, errc)
}

func redactEvidence(red *redact.Redactor, ev *checks.Evidence) {
	ev.Target = red.Redact(ev.Target)
	ev.Expected = red.Redact(ev.Expected)
	ev.Actual = red.Redact(ev.Actual)
}

func errorStep(res StepResult, summary string) StepResult {
	res.Status = checks.Error
	res.Summary = summary
	return res
}
