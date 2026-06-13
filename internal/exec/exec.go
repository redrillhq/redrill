package exec

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alyamovsky/drillbit/internal/checks"
	"github.com/alyamovsky/drillbit/internal/config"
	"github.com/alyamovsky/drillbit/internal/driver"
	"github.com/alyamovsky/drillbit/internal/driver/borg"
	"github.com/alyamovsky/drillbit/internal/driver/dumpdir"
	"github.com/alyamovsky/drillbit/internal/redact"
	"github.com/alyamovsky/drillbit/internal/sandbox"
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
	L1     *config.L1    `json:"l1,omitempty"`
	L2     *config.L2    `json:"l2,omitempty"`
	L3     *config.L3    `json:"l3,omitempty"`
	Now    time.Time     `json:"now"`

	// Set for restore levels (L2/L3): where to restore, and the previous proven
	// run's restored file count (the file_count_tolerance baseline, read from the
	// store by the orchestrator — checks never touch the store).
	Scratch       config.Scratch `json:"scratch"`
	PrevFileCount int            `json:"prev_file_count"`
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
	host       string
	borgRunner borg.Runner            // nil = the real borg binary; tests inject a fake
	sandbox    sandbox.SandboxRuntime // nil = no L3 runtime → L3 skipped
}

// NewLocal returns a LocalExecutor identifying itself as host.
func NewLocal(host string) *LocalExecutor { return &LocalExecutor{host: host} }

// WithSandbox sets the L3 sandbox runtime (e.g. the Docker runtime). Without
// one, L3 is reported skipped, never pass.
func (e *LocalExecutor) WithSandbox(rt sandbox.SandboxRuntime) *LocalExecutor {
	e.sandbox = rt
	return e
}

func (e *LocalExecutor) Describe() ExecutorInfo { return ExecutorInfo{Host: e.host} }

// RunStep dispatches by (level, source type). Unimplemented combinations return
// a wrapped ErrUnsupported; every other outcome — including "the backup is bad"
// (fail) and "couldn't check" (error) — is reported in StepResult with a nil
// error.
func (e *LocalExecutor) RunStep(ctx context.Context, step StepSpec) (StepResult, error) {
	switch {
	case step.Level == "l1" && step.Source.Type == "dumpdir":
		return runDumpdirL1(ctx, step)
	case step.Level == "l1" && step.Source.Type == "borg":
		return e.runBorgL1(ctx, step)
	case step.Level == "l2" && step.Source.Type == "borg":
		return e.runBorgL2(ctx, step)
	case step.Level == "l2" && step.Source.Type == "dumpdir":
		return runDumpdirL2(ctx, step)
	case step.Level == "l3" && step.Source.Type == "dumpdir":
		return e.runDumpdirL3(ctx, step)
	case step.Level == "l3" && step.Source.Type == "borg":
		return e.runBorgL3(ctx, step)
	default:
		return StepResult{}, fmt.Errorf("%w: level %q source %q", ErrUnsupported, step.Level, step.Source.Type)
	}
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

// runBorgL1 runs L1 against a borg repo: native check (engine delegation),
// snapshot recency, and size-anomaly detection. Secrets are resolved locally
// (the remotable design) and fed to the redactor so nothing leaks into evidence.
func (e *LocalExecutor) runBorgL1(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	src := step.Source

	passphrase, err := resolveSecret(src.PassphraseFile, src.PassphraseEnv)
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	red := redact.New()
	red.AddSecret(passphrase)

	d := borg.New(src.Repo,
		borg.WithBinary(src.Binary),
		borg.WithPassphrase(passphrase),
		borg.WithSSHKey(src.SSHKeyFile),
		borg.WithRunner(e.borgRunner),
	)
	if err := d.Validate(ctx); err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}

	l1 := step.L1
	if l1 == nil {
		res.Status, res.Summary = checks.Pass, "no L1 checks configured"
		return res, nil
	}
	if l1.NativeCheck != nil && *l1.NativeCheck {
		res.Evidence = append(res.Evidence, nativeCheckEvidence(ctx, d, red))
	}
	if l1.SnapshotMaxAge != nil || l1.SizeAnomalyPct != nil {
		res.Evidence = append(res.Evidence, borgArchiveChecks(ctx, d, l1, step.Now, red)...)
	}

	res.Status = aggregate(res.Evidence)
	res.Summary = red.Redact(summarize(res.Status, res.Evidence))
	return res, nil
}

// nativeCheckEvidence turns `borg check` into one evidence row: OK → pass, errors
// found → fail (the repo is corrupt), couldn't run → error.
func nativeCheckEvidence(ctx context.Context, d *borg.Driver, red *redact.Redactor) checks.Evidence {
	ev := checks.Evidence{Kind: "native_check", Target: "repository", Expected: "borg check passes"}
	rep, err := d.NativeCheck(ctx, driver.NativeCheckOpts{})
	switch {
	case err != nil:
		ev.Status, ev.Actual = checks.Error, err.Error()
	case rep.OK:
		ev.Status, ev.Actual = checks.Pass, rep.Summary
	default:
		ev.Status, ev.Actual = checks.Fail, rep.Summary
	}
	redactEvidence(red, &ev)
	return ev
}

func borgArchiveChecks(ctx context.Context, d *borg.Driver, l1 *config.L1, now time.Time, red *redact.Redactor) []checks.Evidence {
	snaps, err := d.ListSnapshots(ctx)
	if err != nil {
		return []checks.Evidence{{Kind: "snapshot_max_age", Target: "repository", Status: checks.Error, Actual: red.Redact(err.Error())}}
	}
	var out []checks.Evidence
	if l1.SnapshotMaxAge != nil {
		var newest time.Time
		if len(snaps) > 0 {
			newest = snaps[0].Time // ListSnapshots is newest-first
		}
		ev, _ := checks.SnapshotMaxAge{Newest: newest, Max: l1.SnapshotMaxAge.Duration()}.Run(ctx, checks.CheckEnv{Now: now})
		redactEvidence(red, &ev)
		out = append(out, ev)
	}
	if l1.SizeAnomalyPct != nil {
		if latest, trailing, ok := borgSizes(ctx, d, snaps); ok {
			ev, _ := checks.SizeAnomaly{LatestSize: latest, TrailingSizes: trailing, Pct: *l1.SizeAnomalyPct}.Run(ctx, checks.CheckEnv{})
			redactEvidence(red, &ev)
			out = append(out, ev)
		}
	}
	return out
}

// borgSizes fetches the latest archive's size plus a bounded trailing window for
// the anomaly check. It is best-effort and advisory; if even the latest size is
// unavailable it reports ok=false so the size check is simply skipped.
func borgSizes(ctx context.Context, d *borg.Driver, snaps []driver.Snapshot) (int64, []int64, bool) {
	const window = 7
	if len(snaps) == 0 {
		return 0, nil, false
	}
	latest, err := d.ArchiveSize(ctx, snaps[0].ID)
	if err != nil {
		return 0, nil, false
	}
	var trailing []int64
	for i := 1; i < len(snaps) && i <= window; i++ {
		if sz, e := d.ArchiveSize(ctx, snaps[i].ID); e == nil {
			trailing = append(trailing, sz)
		}
	}
	return latest, trailing, true
}

// resolveSecret reads a secret value from a *_file (file contents, trailing
// newline trimmed) or *_env (env var) reference. Neither set yields "".
func resolveSecret(file, env string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file) //nolint:gosec // G304: secret-file path is operator-configured
		if err != nil {
			return "", fmt.Errorf("read secret file %s: %w", file, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	if env != "" {
		return os.Getenv(env), nil
	}
	return "", nil
}

// runBorgL2 restores a sample (or the include_paths) of the newest archive into
// scratch and runs the L2 checks against it.
func (e *LocalExecutor) runBorgL2(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	l2 := step.L2
	if l2 == nil {
		res.Status, res.Summary = checks.Pass, "no L2 config"
		return res, nil
	}
	src := step.Source
	passphrase, err := resolveSecret(src.PassphraseFile, src.PassphraseEnv)
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	red := redact.New()
	red.AddSecret(passphrase)

	d := borg.New(src.Repo,
		borg.WithBinary(src.Binary), borg.WithPassphrase(passphrase),
		borg.WithSSHKey(src.SSHKeyFile), borg.WithRunner(e.borgRunner),
	)
	if err := d.Validate(ctx); err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	snaps, err := d.ListSnapshots(ctx)
	if err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	if len(snaps) == 0 {
		return errorStep(res, "no archives in repository"), nil
	}
	archive := snaps[0].ID // newest

	var paths []string
	var predicted int64
	if l2.Restore.Scope == "full" {
		predicted, _ = d.ArchiveSize(ctx, archive) // best-effort; the quota still bounds it
	} else {
		files, err := d.ListFiles(ctx, archive)
		if err != nil {
			return errorStep(res, red.Redact(err.Error())), nil
		}
		paths, predicted = selectSample(files, l2.Restore.Sample, l2.Restore.IncludePaths, uint64(step.RunID)) //nolint:gosec // G115: run ids are positive
	}

	sc, err := newScratch(step.Scratch.Dir, step.RunID, step.Scratch.MaxBytes.Bytes())
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	defer sc.cleanup()
	if err := sc.preflight(predicted); err != nil {
		return errorStep(res, err.Error()), nil
	}
	if _, err := d.Restore(ctx, driver.Selection{SnapshotIDs: []string{archive}, Paths: paths}, sc.root); err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	return finishL2(ctx, res, sc.root, l2.Checks, step, red), nil
}

// runDumpdirL2 copies the picked dump file(s) into scratch and runs L2 checks.
func runDumpdirL2(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	l2 := step.L2
	if l2 == nil {
		res.Status, res.Summary = checks.Pass, "no L2 config"
		return res, nil
	}
	red := redact.New()
	d := dumpdir.New(step.Source.Path, step.Source.Pattern)
	if err := d.Validate(ctx); err != nil {
		return errorStep(res, err.Error()), nil
	}
	snaps, err := d.ListSnapshots(ctx)
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	if len(snaps) == 0 {
		return errorStep(res, fmt.Sprintf("no files match %q in %s", step.Source.Pattern, step.Source.Path)), nil
	}
	selected := snaps[:1]
	if step.Source.Pick == "all-matching-window" {
		selected = snaps
	}
	var ids []string
	var predicted int64
	for _, s := range selected {
		ids = append(ids, s.ID)
		predicted += s.Size
	}

	sc, err := newScratch(step.Scratch.Dir, step.RunID, step.Scratch.MaxBytes.Bytes())
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	defer sc.cleanup()
	if err := sc.preflight(predicted); err != nil {
		return errorStep(res, err.Error()), nil
	}
	if _, err := d.Restore(ctx, driver.Selection{SnapshotIDs: ids}, sc.root); err != nil {
		return errorStep(res, err.Error()), nil
	}
	return finishL2(ctx, res, sc.root, l2.Checks, step, red), nil
}

// finishL2 walks the restored tree once for aggregates, runs the configured L2
// checks, and fills the result.
func finishL2(ctx context.Context, res StepResult, restoreDir string, cfgChecks []config.Check, step StepSpec, red *redact.Redactor) StepResult {
	count, total, newest := walkAggregates(restoreDir)
	env := checks.CheckEnv{RestoreDir: restoreDir, Now: step.Now}
	for _, cc := range cfgChecks {
		c := buildL2Check(cc, count, total, newest, step.PrevFileCount)
		if c == nil {
			continue // exec checks (Phase 3) aren't wired yet
		}
		ev, err := c.Run(ctx, env)
		if err != nil {
			ev = checks.Evidence{Kind: c.Kind(), Status: checks.Error, Actual: err.Error()}
		}
		redactEvidence(red, &ev)
		res.Evidence = append(res.Evidence, ev)
	}
	res.Files, res.Bytes = count, total
	res.Status = aggregate(res.Evidence)
	res.Summary = red.Redact(summarize(res.Status, res.Evidence))
	return res
}

// buildL2Check maps a config check to its runtime check, feeding aggregate-based
// kinds the values computed once over the restore.
func buildL2Check(cc config.Check, count int, total int64, newest time.Time, prev int) checks.Check {
	switch cc.Kind {
	case "path_exists":
		return checks.PathExists{Path: cc.Path}
	case "path_absent":
		return checks.PathAbsent{Path: cc.Path}
	case "canary_file":
		return checks.CanaryFile{Path: cc.Path}
	case "hash_match":
		return checks.HashMatch{} // borg exposes no per-file manifest → engine-verified
	case "newest_file_max_age":
		return checks.NewestFileMaxAge{Newest: newest, Max: cc.NewestFileMaxAge.Duration()}
	case "min_total_bytes":
		return checks.MinTotalBytes{Total: total, Min: cc.MinTotalBytes.Bytes()}
	case "file_count_tolerance_pct":
		return checks.FileCountTolerance{Count: count, Prev: prev, Pct: cc.FileCountTolerancePct}
	}
	return nil
}

// walkAggregates returns the restored regular-file count, total bytes, and the
// newest mtime.
func walkAggregates(dir string) (int, int64, time.Time) {
	var count int
	var total int64
	var newest time.Time
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil //nolint:nilerr // a walk error on one entry shouldn't abort the aggregate
		}
		info, err := e.Info()
		if err != nil {
			return nil //nolint:nilerr // skip entries that vanished mid-walk
		}
		count++
		total += info.Size()
		if mt := info.ModTime(); mt.After(newest) {
			newest = mt
		}
		return nil
	})
	return count, total, newest
}
