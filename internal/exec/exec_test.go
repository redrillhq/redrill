// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/checks"
	"github.com/alyamovsky/redrill/internal/config"
)

var base = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func ptrSize(b int64) *config.Size            { s := config.Size(b); return &s }
func ptrDur(d time.Duration) *config.Duration { x := config.Duration(d); return &x }
func ptrBool(b bool) *bool                    { return &b }

func makeGz(t *testing.T, dir, name, body string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func dumpdirStep(dir string, l1 *config.L1, now time.Time) StepSpec {
	return StepSpec{
		RunID:  1,
		Drill:  "app-db",
		Level:  "l1",
		Source: config.Source{Name: "dumps", Type: "dumpdir", Path: dir, Pattern: "*.sql.gz", Pick: "newest"},
		L1:     l1,
		Now:    now,
	}
}

func fullL1() *config.L1 {
	return &config.L1{FileMinBytes: ptrSize(1), CompressionTest: ptrBool(true), MaxAge: ptrDur(36 * time.Hour)}
}

func TestRunStepDumpdirL1Pass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-1*time.Hour))

	res, err := NewLocal("h").RunStep(context.Background(), dumpdirStep(dir, fullL1(), base))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s, want pass (%s)", res.Status, res.Summary)
	}
	if len(res.Evidence) != 3 || res.Files != 1 {
		t.Errorf("got %d evidence / %d files, want 3 / 1", len(res.Evidence), res.Files)
	}
	for _, ev := range res.Evidence {
		if ev.Status != checks.Pass {
			t.Errorf("evidence %s = %s, want pass", ev.Kind, ev.Status)
		}
	}
}

func TestRunStepDumpdirL1FailStale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base.Add(-30*24*time.Hour)) // stale

	res, err := NewLocal("h").RunStep(context.Background(), dumpdirStep(dir, fullL1(), base))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	var maxAge checks.Evidence
	for _, ev := range res.Evidence {
		if ev.Kind == "max_age" {
			maxAge = ev
		}
	}
	if maxAge.Status != checks.Fail {
		t.Errorf("max_age = %s, want fail", maxAge.Status)
	}
}

func TestRunStepDumpdirL1ErrorUnreadable(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope")
	res, err := NewLocal("h").RunStep(context.Background(), dumpdirStep(missing, fullL1(), base))
	if err != nil {
		t.Fatalf("RunStep returned Go error, want StepResult{error}: %v", err)
	}
	if res.Status != checks.Error {
		t.Errorf("status = %s, want error (unreadable dir)", res.Status)
	}
}

func TestRunStepNoFilesIsError(t *testing.T) {
	t.Parallel()
	res, err := NewLocal("h").RunStep(context.Background(), dumpdirStep(t.TempDir(), fullL1(), base))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != checks.Error {
		t.Errorf("status = %s, want error (no matching files)", res.Status)
	}
}

func TestRunStepUnsupported(t *testing.T) {
	t.Parallel()
	cases := []StepSpec{
		{Level: "l1", Source: config.Source{Type: "unknown"}},
		{Level: "l4", Source: config.Source{Type: "borg"}},
		{Level: "l2", Source: config.Source{Type: ""}},
	}
	for _, step := range cases {
		_, err := NewLocal("h").RunStep(context.Background(), step)
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("(%s,%s) err = %v, want ErrUnsupported", step.Level, step.Source.Type, err)
		}
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	if got := NewLocal("nas").Describe(); got.Host != "nas" {
		t.Errorf("Describe().Host = %q, want nas", got.Host)
	}
}

// StepSpec/StepResult must stay serializable: no func fields, channels, or handles.
func TestStepSpecSerializable(t *testing.T) {
	t.Parallel()
	spec := dumpdirStep("/backups/pg", fullL1(), base)
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal StepSpec: %v", err)
	}
	var got StepSpec
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal StepSpec: %v", err)
	}
	if got.Source.Path != spec.Source.Path || got.L1.FileMinBytes.Bytes() != 1 || !got.Now.Equal(base) {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestStepResultSerializable(t *testing.T) {
	t.Parallel()
	res := StepResult{
		Level:    "l1",
		Status:   checks.Fail,
		Evidence: []checks.Evidence{{Kind: "max_age", Target: "x.sql.gz", Expected: "age <= 36h0m0s", Actual: "age 720h", Status: checks.Fail}},
		Summary:  "fail: 1 checks (0 pass, 1 fail, 0 error)",
		Files:    1,
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal StepResult: %v", err)
	}
	var got StepResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal StepResult: %v", err)
	}
	if got.Status != checks.Fail || len(got.Evidence) != 1 || got.Evidence[0].Kind != "max_age" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

type fakeBorg struct {
	validateExit  int    // borg list --short
	listExit      int    // borg list --json
	checkExit     int    // borg check
	listJSON      string // borg list --json stdout
	listFilesJSON string // borg list --json-lines stdout
	checkStderr   string // borg check stderr
}

func (f fakeBorg) run(_ context.Context, _ string, _ []string, _ string, args []string) ([]byte, []byte, int, error) {
	if len(args) == 0 {
		return nil, nil, 0, nil
	}
	switch args[0] {
	case "list":
		for _, a := range args {
			switch a {
			case "--short":
				return nil, []byte("validate error"), f.validateExit, nil
			case "--json-lines":
				return []byte(f.listFilesJSON), nil, 0, nil
			}
		}
		return []byte(f.listJSON), []byte("list error"), f.listExit, nil
	case "check":
		return nil, []byte(f.checkStderr), f.checkExit, nil
	case "info":
		return []byte(`{"archives":[{"stats":{"original_size":1000}}]}`), nil, 0, nil
	case "extract":
		return nil, nil, 0, nil // can't restore real files; L2 success is integration-tested
	}
	return nil, nil, 0, nil
}

func borgFilesJSON(sizes ...int64) string {
	var b strings.Builder
	for i, sz := range sizes {
		fmt.Fprintf(&b, `{"type":"-","path":"data/f%d.txt","size":%d,"mtime":"2026-06-13T12:00:00.000000"}`+"\n", i, sz)
	}
	return b.String()
}

func borgExecutor(f fakeBorg) *LocalExecutor {
	e := NewLocal("h")
	e.borgRunner = f.run
	return e
}

// borgListJSON renders archives oldest→newest, as borg does.
func borgListJSON(times ...time.Time) string {
	var b strings.Builder
	b.WriteString(`{"archives":[`)
	for i, tm := range times {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"arch-%d","time":%q}`, i+1, tm.In(time.Local).Format("2006-01-02T15:04:05.000000"))
	}
	b.WriteString(`]}`)
	return b.String()
}

func borgL1() *config.L1 {
	nc := true
	sma := config.Duration(36 * time.Hour)
	return &config.L1{NativeCheck: &nc, SnapshotMaxAge: &sma}
}

func borgStep(l1 *config.L1, now time.Time) StepSpec {
	return StepSpec{Level: "l1", Source: config.Source{Name: "borg1", Type: "borg", Repo: "/r"}, L1: l1, Now: now}
}

func TestRunBorgL1Pass(t *testing.T) {
	t.Parallel()
	f := fakeBorg{checkExit: 0, listJSON: borgListJSON(base.Add(-2*time.Hour), base.Add(-1*time.Hour))}
	res, err := borgExecutor(f).RunStep(context.Background(), borgStep(borgL1(), base))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s, want pass (%s)", res.Status, res.Summary)
	}
	if len(res.Evidence) != 2 {
		t.Errorf("evidence = %d, want 2 (native_check, snapshot_max_age)", len(res.Evidence))
	}
}

// borg check exit 1 = errors found = corrupt repo → fail.
func TestRunBorgL1NativeCheckFail(t *testing.T) {
	t.Parallel()
	f := fakeBorg{checkExit: 1, listJSON: borgListJSON(base.Add(-1 * time.Hour))}
	res, _ := borgExecutor(f).RunStep(context.Background(), borgStep(borgL1(), base))
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if got := evidenceByKind(res, "native_check"); got.Status != checks.Fail {
		t.Errorf("native_check = %s, want fail", got.Status)
	}
}

func TestRunBorgL1StaleFail(t *testing.T) {
	t.Parallel()
	f := fakeBorg{checkExit: 0, listJSON: borgListJSON(base.Add(-30 * 24 * time.Hour))}
	res, _ := borgExecutor(f).RunStep(context.Background(), borgStep(borgL1(), base))
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if got := evidenceByKind(res, "snapshot_max_age"); got.Status != checks.Fail {
		t.Errorf("snapshot_max_age = %s, want fail", got.Status)
	}
}

// Unreachable repo is error, never fail.
func TestRunBorgL1ValidateError(t *testing.T) {
	t.Parallel()
	f := fakeBorg{validateExit: 2}
	res, _ := borgExecutor(f).RunStep(context.Background(), borgStep(borgL1(), base))
	if res.Status != checks.Error {
		t.Errorf("status = %s, want error", res.Status)
	}
}

func TestRunBorgL1ListErrorIsError(t *testing.T) {
	t.Parallel()
	sma := config.Duration(36 * time.Hour)
	l1 := &config.L1{SnapshotMaxAge: &sma} // age check only, no native check
	f := fakeBorg{listExit: 2}
	res, _ := borgExecutor(f).RunStep(context.Background(), borgStep(l1, base))
	if res.Status != checks.Error {
		t.Errorf("status = %s, want error (couldn't list)", res.Status)
	}
}

// A secret echoed in borg output must never reach evidence.
func TestRunBorgL1RedactsSecretInEvidence(t *testing.T) {
	t.Parallel()
	passFile := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(passFile, []byte("topsecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := fakeBorg{checkExit: 1, checkStderr: "integrity error near topsecret in repo"}
	step := borgStep(borgL1(), base)
	step.Source.PassphraseFile = passFile
	step.Source.Repo = "/r"
	res, _ := borgExecutor(f).RunStep(context.Background(), step)

	ev := evidenceByKind(res, "native_check")
	if strings.Contains(ev.Actual, "topsecret") {
		t.Errorf("secret leaked into evidence: %q", ev.Actual)
	}
	if !strings.Contains(ev.Actual, "[REDACTED]") {
		t.Errorf("expected redaction marker, got %q", ev.Actual)
	}
}

func evidenceByKind(res StepResult, kind string) checks.Evidence {
	for _, ev := range res.Evidence {
		if ev.Kind == kind {
			return ev
		}
	}
	return checks.Evidence{}
}

// A restore that would blow the scratch quota is refused up front: error, never fail.
func TestRunBorgL2ScratchQuotaError(t *testing.T) {
	t.Parallel()
	f := fakeBorg{
		listJSON:      borgListJSON(base.Add(-1 * time.Hour)),
		listFilesJSON: borgFilesJSON(50, 50, 50), // 150 bytes
	}
	l2 := &config.L2{Restore: config.Restore{Scope: "sample", Sample: &config.Sample{Files: 10}}}
	step := StepSpec{
		RunID: 1, Level: "l2", Source: config.Source{Type: "borg", Repo: "/r"}, L2: l2,
		Scratch: config.Scratch{Dir: t.TempDir(), MaxBytes: config.Size(100)}, Now: base,
	}
	res, _ := borgExecutor(f).RunStep(context.Background(), step)
	if res.Status != checks.Error {
		t.Fatalf("status = %s, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "preflight") {
		t.Errorf("summary = %q, want it to cite the preflight", res.Summary)
	}
}

func dumpdirL2Step(dir string, cfgChecks []config.Check, scratchDir string, maxBytes int64) StepSpec {
	return StepSpec{
		RunID:   1,
		Level:   "l2",
		Source:  config.Source{Type: "dumpdir", Path: dir, Pattern: "*.sql.gz", Pick: "newest"},
		L2:      &config.L2{Checks: cfgChecks},
		Scratch: config.Scratch{Dir: scratchDir, MaxBytes: config.Size(maxBytes)},
		Now:     base,
	}
}

// dumpdir restores real files, so L2 success is unit-testable.
func TestRunDumpdirL2Pass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1; -- a healthy dump", base)
	step := dumpdirL2Step(dir, []config.Check{
		{Kind: "path_exists", Path: "app-1.sql.gz"},
		{Kind: "min_total_bytes", MinTotalBytes: config.Size(1)},
	}, t.TempDir(), 0)

	res, err := NewLocal("h").RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s, want pass (%s)", res.Status, res.Summary)
	}
	if res.Files != 1 {
		t.Errorf("files = %d, want 1", res.Files)
	}
}

func TestRunDumpdirL2PathMissingFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "SELECT 1;", base)
	step := dumpdirL2Step(dir, []config.Check{{Kind: "path_exists", Path: "data/missing"}}, t.TempDir(), 0)

	res, _ := NewLocal("h").RunStep(context.Background(), step)
	if res.Status != checks.Fail {
		t.Errorf("status = %s, want fail (path missing)", res.Status)
	}
}

func TestRunDumpdirL2ScratchQuotaError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeGz(t, dir, "app-1.sql.gz", "content larger than one byte", base)
	step := dumpdirL2Step(dir, []config.Check{{Kind: "min_total_bytes", MinTotalBytes: config.Size(1)}}, t.TempDir(), 1)

	res, _ := NewLocal("h").RunStep(context.Background(), step)
	if res.Status != checks.Error {
		t.Errorf("status = %s, want error (scratch quota)", res.Status)
	}
}

// --- restic glue (fake runner) ---

type fakeRestic struct {
	snapshotsExit int
	checkExit     int
	snapshotsJSON string
	lsJSON        string
	statsJSON     string
	checkStderr   string
	stageRel      string // restore creates <target>/src/<stageRel> if set
}

func (f fakeRestic) run(_ context.Context, _ string, _ []string, _ string, args []string) ([]byte, []byte, int, error) {
	switch resticSub(args) {
	case "snapshots":
		return []byte(f.snapshotsJSON), []byte("snapshots error"), f.snapshotsExit, nil
	case "check":
		return nil, []byte(f.checkStderr), f.checkExit, nil
	case "ls":
		return []byte(f.lsJSON), nil, 0, nil
	case "stats":
		return []byte(f.statsJSON), nil, 0, nil
	case "restore":
		if target := flagVal(args, "--target"); target != "" && f.stageRel != "" {
			p := filepath.Join(target, "src", filepath.FromSlash(f.stageRel))
			_ = os.MkdirAll(filepath.Dir(p), 0o700)
			_ = os.WriteFile(p, []byte("payload bytes"), 0o600)
		}
		return nil, nil, 0, nil
	}
	return nil, nil, 0, nil
}

// resticSub returns the subcommand, skipping the global "-r <repo>" pair and flag
// values so a repo or include path is never mistaken for the subcommand.
func resticSub(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-r", "--target", "--include", "--limit-download", "--mode":
			i++
		default:
			if !strings.HasPrefix(args[i], "-") {
				return args[i]
			}
		}
	}
	return ""
}

func flagVal(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func resticExecutor(f fakeRestic) *LocalExecutor {
	e := NewLocal("h")
	e.resticRunner = f.run
	return e
}

// resticSnapshotsJSON renders a snapshots array (oldest→newest, as restic does);
// the driver sorts it newest-first.
func resticSnapshotsJSON(times ...time.Time) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, tm := range times {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"time":%q,"paths":["/src"],"id":"id%d0000","short_id":"id%d"}`, tm.UTC().Format(time.RFC3339Nano), i, i)
	}
	b.WriteByte(']')
	return b.String()
}

func resticLsJSON(sizes ...int64) string {
	var b strings.Builder
	b.WriteString(`{"paths":["/src"],"struct_type":"snapshot"}` + "\n")
	for i, sz := range sizes {
		fmt.Fprintf(&b, `{"type":"file","path":"/src/data/f%d.txt","size":%d,"mtime":"2026-06-13T12:00:00+00:00","struct_type":"node"}`+"\n", i, sz)
	}
	return b.String()
}

func resticL1() *config.L1 {
	nc := true
	sma := config.Duration(36 * time.Hour)
	return &config.L1{NativeCheck: &nc, SnapshotMaxAge: &sma}
}

func resticStep(l1 *config.L1, now time.Time) StepSpec {
	return StepSpec{Level: "l1", Source: config.Source{Name: "r1", Type: "restic", Repo: "/r"}, L1: l1, Now: now}
}

func TestRunResticL1Pass(t *testing.T) {
	t.Parallel()
	f := fakeRestic{checkExit: 0, snapshotsJSON: resticSnapshotsJSON(base.Add(-2*time.Hour), base.Add(-1*time.Hour))}
	res, err := resticExecutor(f).RunStep(context.Background(), resticStep(resticL1(), base))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s, want pass (%s)", res.Status, res.Summary)
	}
	if len(res.Evidence) != 2 {
		t.Errorf("evidence = %d, want 2 (native_check, snapshot_max_age)", len(res.Evidence))
	}
	if got := evidenceByKind(res, "native_check"); got.Expected != "restic check passes" {
		t.Errorf("native_check expected = %q, want \"restic check passes\"", got.Expected)
	}
}

// restic check non-zero (Validate already proved reachability) = corrupt repo → fail.
func TestRunResticL1NativeCheckFail(t *testing.T) {
	t.Parallel()
	f := fakeRestic{checkExit: 1, snapshotsJSON: resticSnapshotsJSON(base.Add(-1 * time.Hour))}
	res, _ := resticExecutor(f).RunStep(context.Background(), resticStep(resticL1(), base))
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if got := evidenceByKind(res, "native_check"); got.Status != checks.Fail {
		t.Errorf("native_check = %s, want fail", got.Status)
	}
}

func TestRunResticL1StaleFail(t *testing.T) {
	t.Parallel()
	f := fakeRestic{checkExit: 0, snapshotsJSON: resticSnapshotsJSON(base.Add(-30 * 24 * time.Hour))}
	res, _ := resticExecutor(f).RunStep(context.Background(), resticStep(resticL1(), base))
	if res.Status != checks.Fail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if got := evidenceByKind(res, "snapshot_max_age"); got.Status != checks.Fail {
		t.Errorf("snapshot_max_age = %s, want fail", got.Status)
	}
}

// Unreachable repo / bad password surfaces at Validate as error, never fail.
func TestRunResticL1ValidateError(t *testing.T) {
	t.Parallel()
	f := fakeRestic{snapshotsExit: 1}
	res, _ := resticExecutor(f).RunStep(context.Background(), resticStep(resticL1(), base))
	if res.Status != checks.Error {
		t.Errorf("status = %s, want error", res.Status)
	}
}

// A secret echoed in restic output must never reach evidence.
func TestRunResticL1RedactsSecretInEvidence(t *testing.T) {
	t.Parallel()
	passFile := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(passFile, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := fakeRestic{checkExit: 1, checkStderr: "integrity error near s3cr3t in repo", snapshotsJSON: resticSnapshotsJSON(base.Add(-1 * time.Hour))}
	step := resticStep(resticL1(), base)
	step.Source.PasswordFile = passFile
	res, _ := resticExecutor(f).RunStep(context.Background(), step)

	ev := evidenceByKind(res, "native_check")
	if strings.Contains(ev.Actual, "s3cr3t") {
		t.Errorf("secret leaked into evidence: %q", ev.Actual)
	}
	if !strings.Contains(ev.Actual, "[REDACTED]") {
		t.Errorf("expected redaction marker, got %q", ev.Actual)
	}
}

// Backend secrets (from env_file) are redacted from evidence, but benign values
// like a region are not — AddEnv discriminates by key name.
func TestRunResticL1RedactsBackendSecret(t *testing.T) {
	t.Parallel()
	envFile := filepath.Join(t.TempDir(), "b2.env")
	if err := os.WriteFile(envFile, []byte("AWS_SECRET_ACCESS_KEY=supersecretvalue\nAWS_DEFAULT_REGION=us-west-000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := fakeRestic{
		checkExit:     1,
		checkStderr:   "error reaching us-west-000 with key supersecretvalue",
		snapshotsJSON: resticSnapshotsJSON(base.Add(-1 * time.Hour)),
	}
	step := resticStep(resticL1(), base)
	step.Source.EnvFile = envFile
	res, _ := resticExecutor(f).RunStep(context.Background(), step)

	ev := evidenceByKind(res, "native_check")
	if strings.Contains(ev.Actual, "supersecretvalue") {
		t.Errorf("backend secret leaked into evidence: %q", ev.Actual)
	}
	if !strings.Contains(ev.Actual, "us-west-000") {
		t.Errorf("benign region was over-redacted: %q", ev.Actual)
	}
}

// A restore that would blow the scratch quota is refused up front: error, never fail.
func TestRunResticL2ScratchQuotaError(t *testing.T) {
	t.Parallel()
	f := fakeRestic{
		snapshotsJSON: resticSnapshotsJSON(base.Add(-1 * time.Hour)),
		lsJSON:        resticLsJSON(50, 50, 50), // 150 bytes
	}
	l2 := &config.L2{Restore: config.Restore{Scope: "sample", Sample: &config.Sample{Files: 10}}}
	step := StepSpec{
		RunID: 1, Level: "l2", Source: config.Source{Type: "restic", Repo: "/r"}, L2: l2,
		Scratch: config.Scratch{Dir: t.TempDir(), MaxBytes: config.Size(100)}, Now: base,
	}
	res, _ := resticExecutor(f).RunStep(context.Background(), step)
	if res.Status != checks.Error {
		t.Fatalf("status = %s, want error", res.Status)
	}
	if !strings.Contains(res.Summary, "preflight") {
		t.Errorf("summary = %q, want it to cite the preflight", res.Summary)
	}
}

// restic restore strips the snapshot root, so the L2 checks see relative paths.
func TestRunResticL2Pass(t *testing.T) {
	t.Parallel()
	f := fakeRestic{
		snapshotsJSON: resticSnapshotsJSON(base.Add(-1 * time.Hour)),
		lsJSON:        resticLsJSON(50),
		stageRel:      "data/docs/a.txt",
	}
	l2 := &config.L2{
		Restore: config.Restore{Scope: "sample", Sample: &config.Sample{Files: 10}},
		Checks: []config.Check{
			{Kind: "path_exists", Path: "data/docs/a.txt"},
			{Kind: "min_total_bytes", MinTotalBytes: config.Size(1)},
		},
	}
	step := StepSpec{
		RunID: 1, Level: "l2", Source: config.Source{Type: "restic", Repo: "/r"}, L2: l2,
		Scratch: config.Scratch{Dir: t.TempDir()}, Now: base,
	}
	res, err := resticExecutor(f).RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Status != checks.Pass {
		t.Fatalf("status = %s, want pass (%s)", res.Status, res.Summary)
	}
	if res.Files != 1 {
		t.Errorf("files = %d, want 1", res.Files)
	}
}

func TestAggregatePrecedence(t *testing.T) {
	t.Parallel()
	ev := func(s checks.Status) checks.Evidence { return checks.Evidence{Status: s} }
	tests := []struct {
		name string
		in   []checks.Evidence
		want checks.Status
	}{
		{"all pass", []checks.Evidence{ev(checks.Pass), ev(checks.Pass)}, checks.Pass},
		{"fail dominates error", []checks.Evidence{ev(checks.Error), ev(checks.Fail)}, checks.Fail},
		{"error over pass", []checks.Evidence{ev(checks.Pass), ev(checks.Error)}, checks.Error},
		{"empty is pass", nil, checks.Pass},
	}
	for _, tt := range tests {
		if got := aggregate(tt.in); got != tt.want {
			t.Errorf("%s: aggregate = %s, want %s", tt.name, got, tt.want)
		}
	}
}
