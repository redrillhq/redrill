// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

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

	"github.com/redrillhq/redrill/internal/checks"
	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/driver"
	"github.com/redrillhq/redrill/internal/driver/borg"
	"github.com/redrillhq/redrill/internal/driver/dumpdir"
	"github.com/redrillhq/redrill/internal/driver/restic"
	"github.com/redrillhq/redrill/internal/redact"
	"github.com/redrillhq/redrill/internal/sandbox"
)

// ErrUnsupported (wrapped) records the level as skipped, not failed.
var ErrUnsupported = errors.New("unsupported step")

// StepSpec must stay serializable: no func fields, channels, or handles. Secrets
// travel only as the *_file/*_env references inside Source.
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
	// run's restored file count (the file_count_tolerance baseline).
	Scratch       config.Scratch `json:"scratch"`
	PrevFileCount int            `json:"prev_file_count"`
}

type StepResult struct {
	Level    string            `json:"level"`
	Status   checks.Status     `json:"status"`
	Evidence []checks.Evidence `json:"evidence,omitempty"`
	Summary  string            `json:"summary"`
	Files    int               `json:"files"`
	Bytes    int64             `json:"bytes"`
}

type ExecutorInfo struct {
	Host string `json:"host"`
}

type Executor interface {
	Describe() ExecutorInfo
	RunStep(ctx context.Context, step StepSpec) (StepResult, error)
}

type LocalExecutor struct {
	host         string
	borgRunner   borg.Runner            // nil = the real borg binary; tests inject a fake
	resticRunner restic.Runner          // nil = the real restic binary; tests inject a fake
	sandbox      sandbox.SandboxRuntime // nil = no L3 runtime → L3 skipped
	io           IOPolicy
}

func NewLocal(host string) *LocalExecutor { return &LocalExecutor{host: host} }

// WithSandbox sets the L3 runtime. Without one, L3 is skipped, never pass.
func (e *LocalExecutor) WithSandbox(rt sandbox.SandboxRuntime) *LocalExecutor {
	e.sandbox = rt
	return e
}

// WithIOPolicy applies nice/ionice and bandwidth limits to spawned engines.
func (e *LocalExecutor) WithIOPolicy(p IOPolicy) *LocalExecutor {
	e.io = p
	return e
}

// newBorg builds the borg driver with the IO policy applied.
func (e *LocalExecutor) newBorg(src config.Source, passphrase string) *borg.Driver {
	base := e.borgRunner
	if base == nil {
		base = borg.ExecRunner
	}
	return borg.New(src.Repo,
		borg.WithBinary(src.Binary),
		borg.WithPassphrase(passphrase),
		borg.WithSSHKey(src.SSHKeyFile),
		borg.WithUploadRateLimit(e.io.BandwidthKiB),
		borg.WithRunner(wrapIO(base, e.io)),
	)
}

// newRestic builds the restic driver with the IO policy applied.
func (e *LocalExecutor) newRestic(src config.Source, password string, backendEnv map[string]string) *restic.Driver {
	base := e.resticRunner
	if base == nil {
		base = restic.ExecRunner
	}
	return restic.New(src.Repo,
		restic.WithBinary(src.Binary),
		restic.WithPassword(password),
		restic.WithBackendEnv(backendEnv),
		restic.WithDownloadRateLimit(e.io.BandwidthKiB),
		restic.WithRunner(wrapIO(base, e.io)),
	)
}

func (e *LocalExecutor) Describe() ExecutorInfo { return ExecutorInfo{Host: e.host} }

// RunStep returns wrapped ErrUnsupported for unimplemented (level, source type);
// fail and error outcomes are reported in StepResult with a nil error.
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
	case step.Level == "l1" && step.Source.Type == "restic":
		return e.runResticL1(ctx, step)
	case step.Level == "l2" && step.Source.Type == "restic":
		return e.runResticL2(ctx, step)
	case step.Level == "l3" && step.Source.Type == "restic":
		return e.runResticL3(ctx, step)
	default:
		return StepResult{}, fmt.Errorf("%w: level %q source %q", ErrUnsupported, step.Level, step.Source.Type)
	}
}

// ValidateSource checks a source is reachable.
func ValidateSource(ctx context.Context, src config.Source) error {
	switch src.Type {
	case "dumpdir":
		return dumpdir.New(src.Path, src.Pattern).Validate(ctx)
	case "borg":
		passphrase, err := resolveSecret(src.PassphraseFile, src.PassphraseEnv)
		if err != nil {
			return err
		}
		red := redact.New()
		red.AddSecret(passphrase)
		d := borg.New(src.Repo,
			borg.WithBinary(src.Binary), borg.WithPassphrase(passphrase), borg.WithSSHKey(src.SSHKeyFile),
		)
		if err := d.Validate(ctx); err != nil {
			return errors.New(red.Redact(err.Error()))
		}
		return nil
	case "restic":
		password, err := resolveSecret(src.PasswordFile, src.PasswordEnv)
		if err != nil {
			return err
		}
		backendEnv, err := resolveBackendEnv(src.EnvFile)
		if err != nil {
			return err
		}
		red := redact.New()
		red.AddSecret(password)
		for k, v := range backendEnv {
			red.AddEnv(k, v)
		}
		d := restic.New(src.Repo,
			restic.WithBinary(src.Binary), restic.WithPassword(password), restic.WithBackendEnv(backendEnv),
		)
		if err := d.Validate(ctx); err != nil {
			return errors.New(red.Redact(err.Error()))
		}
		return nil
	default:
		return fmt.Errorf("unknown source type %q", src.Type)
	}
}

func runDumpdirL1(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
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

	selected := snaps[:1]
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

// aggregate: fail dominates error, error dominates pass.
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

func (e *LocalExecutor) runBorgL1(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	src := step.Source

	passphrase, err := resolveSecret(src.PassphraseFile, src.PassphraseEnv)
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	red := redact.New()
	red.AddSecret(passphrase)

	d := e.newBorg(src, passphrase)
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

// resticDriver resolves restic secrets, seeds a redactor with them, and builds
// the driver with the IO policy applied.
func (e *LocalExecutor) resticDriver(src config.Source) (*restic.Driver, *redact.Redactor, error) {
	password, err := resolveSecret(src.PasswordFile, src.PasswordEnv)
	if err != nil {
		return nil, nil, err
	}
	backendEnv, err := resolveBackendEnv(src.EnvFile)
	if err != nil {
		return nil, nil, err
	}
	red := redact.New()
	red.AddSecret(password)
	for k, v := range backendEnv {
		red.AddEnv(k, v) // redact secret-named values (keys/tokens), not benign ones (region, endpoint)
	}
	return e.newRestic(src, password, backendEnv), red, nil
}

func (e *LocalExecutor) runResticL1(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	d, red, err := e.resticDriver(step.Source)
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
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
		res.Evidence = append(res.Evidence, resticArchiveChecks(ctx, d, l1, step.Now, red)...)
	}

	res.Status = aggregate(res.Evidence)
	res.Summary = red.Redact(summarize(res.Status, res.Evidence))
	return res, nil
}

// nativeChecker is the slice of a SourceDriver the native-check evidence needs.
type nativeChecker interface {
	Name() string
	NativeCheck(ctx context.Context, opts driver.NativeCheckOpts) (driver.Report, error)
}

// nativeCheckEvidence: OK → pass, errors → fail, couldn't run → error.
func nativeCheckEvidence(ctx context.Context, d nativeChecker, red *redact.Redactor) checks.Evidence {
	ev := checks.Evidence{Kind: "native_check", Target: "repository", Expected: d.Name() + " check passed"}
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
	return archiveAgeAndSize(ctx, snaps, l1, now, red, d.ArchiveSize)
}

func resticArchiveChecks(ctx context.Context, d *restic.Driver, l1 *config.L1, now time.Time, red *redact.Redactor) []checks.Evidence {
	snaps, err := d.ListSnapshots(ctx)
	if err != nil {
		return []checks.Evidence{{Kind: "snapshot_max_age", Target: "repository", Status: checks.Error, Actual: red.Redact(err.Error())}}
	}
	return archiveAgeAndSize(ctx, snaps, l1, now, red, d.SnapshotSize)
}

// archiveAgeAndSize builds the snapshot_max_age and size_anomaly evidence shared
// by the repository engines (borg, restic); sizeFn fetches a snapshot's size.
func archiveAgeAndSize(ctx context.Context, snaps []driver.Snapshot, l1 *config.L1, now time.Time, red *redact.Redactor, sizeFn func(context.Context, string) (int64, error)) []checks.Evidence {
	var out []checks.Evidence
	if l1.SnapshotMaxAge != nil {
		var newest time.Time
		if len(snaps) > 0 {
			newest = snaps[0].Time // newest-first
		}
		ev, _ := checks.SnapshotMaxAge{Newest: newest, Max: l1.SnapshotMaxAge.Duration()}.Run(ctx, checks.CheckEnv{Now: now})
		redactEvidence(red, &ev)
		out = append(out, ev)
	}
	if l1.SizeAnomalyPct != nil {
		if latest, trailing, ok := snapSizes(ctx, snaps, sizeFn); ok {
			ev, _ := checks.SizeAnomaly{LatestSize: latest, TrailingSizes: trailing, Pct: *l1.SizeAnomalyPct}.Run(ctx, checks.CheckEnv{})
			redactEvidence(red, &ev)
			out = append(out, ev)
		}
	}
	return out
}

// snapSizes is best-effort: an unavailable latest size yields ok=false.
func snapSizes(ctx context.Context, snaps []driver.Snapshot, sizeFn func(context.Context, string) (int64, error)) (int64, []int64, bool) {
	const window = 7
	if len(snaps) == 0 {
		return 0, nil, false
	}
	latest, err := sizeFn(ctx, snaps[0].ID)
	if err != nil {
		return 0, nil, false
	}
	var trailing []int64
	for i := 1; i < len(snaps) && i <= window; i++ {
		if sz, e := sizeFn(ctx, snaps[i].ID); e == nil {
			trailing = append(trailing, sz)
		}
	}
	return latest, trailing, true
}

// resolveSecret reads from a *_file (trailing newline trimmed) or *_env; neither
// set yields "".
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

// resolveBackendEnv parses a dotenv-style file (KEY=VALUE per line) of backend
// credentials (S3/B2 keys); an empty path yields a nil map.
func resolveBackendEnv(file string) (map[string]string, error) {
	if file == "" {
		return nil, nil
	}
	b, err := os.ReadFile(file) //nolint:gosec // G304: env-file path is operator-configured
	if err != nil {
		return nil, fmt.Errorf("read env file %s: %w", file, err)
	}
	return parseEnvFile(string(b)), nil
}

func parseEnvFile(s string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k = strings.TrimSpace(k); k != "" {
			env[k] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return env
}

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

	d := e.newBorg(src, passphrase)
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
		predicted, _ = d.ArchiveSize(ctx, archive) // best-effort; quota still bounds it
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

func (e *LocalExecutor) runResticL2(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	l2 := step.L2
	if l2 == nil {
		res.Status, res.Summary = checks.Pass, "no L2 config"
		return res, nil
	}
	d, red, err := e.resticDriver(step.Source)
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	if err := d.Validate(ctx); err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	snaps, err := d.ListSnapshots(ctx)
	if err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	if len(snaps) == 0 {
		return errorStep(res, "no snapshots in repository"), nil
	}
	id := snaps[0].ID // newest

	var paths []string
	var predicted int64
	if l2.Restore.Scope == "full" {
		predicted, _ = d.SnapshotSize(ctx, id) // best-effort; quota still bounds it
	} else {
		files, err := d.ListFiles(ctx, id)
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
	if _, err := d.Restore(ctx, driver.Selection{SnapshotIDs: []string{id}, Paths: paths}, sc.root); err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	return finishL2(ctx, res, sc.root, l2.Checks, step, red), nil
}

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

func finishL2(ctx context.Context, res StepResult, restoreDir string, cfgChecks []config.Check, step StepSpec, red *redact.Redactor) StepResult {
	count, total, newest := walkAggregates(restoreDir)
	env := checks.CheckEnv{RestoreDir: restoreDir, Now: step.Now}
	for _, cc := range cfgChecks {
		c := buildL2Check(cc, count, total, newest, step.PrevFileCount)
		if c == nil {
			continue
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

func buildL2Check(cc config.Check, count int, total int64, newest time.Time, prev int) checks.Check {
	switch cc.Kind {
	case "path_exists":
		return checks.PathExists{Path: cc.Path}
	case "path_absent":
		return checks.PathAbsent{Path: cc.Path}
	case "canary_file":
		return checks.CanaryFile{Path: cc.Path}
	case "hash_match":
		return checks.HashMatch{} // borg exposes no per-file manifest
	case "newest_file_max_age":
		return checks.NewestFileMaxAge{Newest: newest, Max: cc.NewestFileMaxAge.Duration()}
	case "min_total_bytes":
		return checks.MinTotalBytes{Total: total, Min: cc.MinTotalBytes.Bytes()}
	case "file_count_tolerance_pct":
		return checks.FileCountTolerance{Count: count, Prev: prev, Pct: cc.FileCountTolerancePct}
	}
	return nil
}

func walkAggregates(dir string) (int, int64, time.Time) {
	var count int
	var total int64
	var newest time.Time
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil //nolint:nilerr // one bad entry shouldn't abort the aggregate
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
