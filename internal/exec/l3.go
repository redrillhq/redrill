package exec

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/alyamovsky/drillbit/internal/checks"
	"github.com/alyamovsky/drillbit/internal/config"
	"github.com/alyamovsky/drillbit/internal/driver"
	"github.com/alyamovsky/drillbit/internal/driver/borg"
	"github.com/alyamovsky/drillbit/internal/driver/dumpdir"
	"github.com/alyamovsky/drillbit/internal/redact"
	"github.com/alyamovsky/drillbit/internal/sandbox"
)

// ErrNoSandboxRuntime signals that L3 can't run because no container runtime is
// available; the orchestrator records the level as skipped — never a pass.
var ErrNoSandboxRuntime = errors.New("no sandbox runtime")

const containerDumpPath = "/tmp/dump"

// runDumpdirL3 boots a postgres sandbox from the newest dump and runs sql checks.
func (e *LocalExecutor) runDumpdirL3(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	if e.sandbox == nil {
		return StepResult{}, ErrNoSandboxRuntime
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
	sc, err := newScratch(step.Scratch.Dir, step.RunID, step.Scratch.MaxBytes.Bytes())
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	defer sc.cleanup()
	return e.loadAndCheck(ctx, step, sc, d.Path(snaps[0].ID), red)
}

// runBorgL3 extracts the configured dump (extract_path) from the newest archive,
// then boots a sandbox from it.
func (e *LocalExecutor) runBorgL3(ctx context.Context, step StepSpec) (StepResult, error) {
	res := StepResult{Level: step.Level}
	if e.sandbox == nil {
		return StepResult{}, ErrNoSandboxRuntime
	}
	if step.L3 == nil || step.L3.ExtractPath == "" {
		return errorStep(res, "borg L3 requires extract_path"), nil
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
	sc, err := newScratch(step.Scratch.Dir, step.RunID, step.Scratch.MaxBytes.Bytes())
	if err != nil {
		return errorStep(res, err.Error()), nil
	}
	defer sc.cleanup()
	extractDir := filepath.Join(sc.root, "extract")
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		return errorStep(res, err.Error()), nil
	}
	if _, err := d.Restore(ctx, driver.Selection{SnapshotIDs: []string{snaps[0].ID}, Paths: []string{step.L3.ExtractPath}}, extractDir); err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}
	return e.loadAndCheck(ctx, step, sc, filepath.Join(extractDir, step.L3.ExtractPath), red)
}

// loadAndCheck is the shared L3 machinery: stage the dump, catch a version trap,
// boot the sandbox, load, and run the sql checks against it.
func (e *LocalExecutor) loadAndCheck(ctx context.Context, step StepSpec, sc *scratch, dumpSrc string, red *redact.Redactor) (StepResult, error) {
	res := StepResult{Level: step.Level}
	l3 := step.L3
	loaded, format, err := stageDump(dumpSrc, sc.root, sc.maxBytes)
	if err != nil {
		return errorStep(res, red.Redact(err.Error())), nil
	}

	imageMajor := pgMajor(l3.Sandbox.Image)
	if format == "plain" && versionTrap(plainDumpMajor(loaded), imageMajor) {
		return versionTrapResult(res, plainDumpMajor(loaded), imageMajor, dumpSrc, red), nil
	}

	sb, err := e.sandbox.Start(ctx, l3Spec(step, loaded))
	if err != nil {
		if errors.Is(err, sandbox.ErrNoRuntime) {
			return StepResult{}, ErrNoSandboxRuntime
		}
		return errorStep(res, red.Redact(err.Error())), nil
	}
	defer func() { _ = sb.Close(ctx) }()

	if format == "custom" {
		if dm := customDumpMajor(ctx, sb); versionTrap(dm, imageMajor) {
			return versionTrapResult(res, dm, imageMajor, dumpSrc, red), nil
		}
	}

	// Snapshot the databases before loading so loadedDB can distinguish the
	// database the dump creates from one the image pre-created (e.g. POSTGRES_DB).
	before := listDatabases(ctx, sb)
	loadEv := loadDump(ctx, sb, resolveLoader(l3.Load, format))
	redactEvidence(red, &loadEv)
	res.Evidence = append(res.Evidence, loadEv)

	db := loadedDB(ctx, sb, before)
	env := checks.CheckEnv{Sandbox: sb, Now: step.Now}
	for _, cc := range l3.Checks {
		c := buildL3Check(cc, db)
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
	res.Status = aggregate(res.Evidence)
	res.Summary = red.Redact(summarize(res.Status, res.Evidence))
	return res, nil
}

func l3Spec(step StepSpec, loadedPath string) sandbox.SandboxSpec {
	l3 := step.L3
	return sandbox.SandboxSpec{
		Image:    l3.Sandbox.Image,
		Env:      sandboxEnv(l3.Sandbox.Env),
		Network:  "none",
		Memory:   l3.Sandbox.Memory.Bytes(),
		Labels:   map[string]string{sandbox.RunLabel: strconv.FormatInt(step.RunID, 10)},
		ReadyCmd: []string{"pg_isready", "-U", "postgres"},
		Files:    []sandbox.FileInject{{HostPath: loadedPath, ContainerPath: containerDumpPath}},
	}
}

func sandboxEnv(cfg map[string]string) map[string]string {
	env := make(map[string]string, len(cfg)+1)
	for k, v := range cfg {
		env[k] = v
	}
	if _, ok := env["POSTGRES_PASSWORD"]; !ok {
		env["POSTGRES_PASSWORD"] = "drillbit"
	}
	return env
}

func buildL3Check(cc config.Check, db string) checks.Check {
	switch cc.Kind {
	case "sql":
		if cc.SQL == nil {
			return nil
		}
		return checks.SQL{Query: cc.SQL.Query, Expect: cc.SQL.Expect, DB: db}
	case "sql_no_error":
		return checks.SQLNoError{Query: cc.SQLNoError, DB: db}
	}
	return nil
}

// resolveLoader picks which loader to run: an explicit load: pg_restore|psql in
// the drill config wins; otherwise (auto) it follows the detected dump format
// (custom → pg_restore, plain → psql).
func resolveLoader(load, format string) string {
	switch load {
	case "pg_restore", "psql":
		return load
	default:
		if format == "custom" {
			return "pg_restore"
		}
		return "psql"
	}
}

// loadDump runs the chosen loader. Load errors are tolerated and counted — the
// sql asserts give the verdict (DESIGN §6); only an inability to run the loader
// at all is an error here.
func loadDump(ctx context.Context, sb sandbox.Sandbox, loader string) checks.Evidence {
	ev := checks.Evidence{Kind: "load", Target: containerDumpPath, Expected: "dump loads"}
	cmd := []string{"psql", "-U", "postgres", "-d", "postgres", "-f", containerDumpPath}
	if loader == "pg_restore" {
		cmd = []string{"pg_restore", "--no-owner", "--no-privileges", "-U", "postgres", "-d", "postgres", containerDumpPath}
	}
	res, err := sb.Exec(ctx, cmd)
	if err != nil {
		ev.Status, ev.Actual = checks.Error, "exec: "+err.Error()
		return ev
	}
	ev.Status = checks.Pass
	ev.Actual = fmt.Sprintf("loaded (exit %d, %d error lines)", res.ExitCode, countErrorLines(res.Stdout+res.Stderr))
	return ev
}

// listDatabases returns the non-template databases present in the sandbox.
func listDatabases(ctx context.Context, sb sandbox.Sandbox) map[string]bool {
	set := map[string]bool{}
	res, err := sb.Exec(ctx, []string{
		"psql", "-U", "postgres", "-tAqX", "-c",
		"select datname from pg_database where not datistemplate order by datname",
	})
	if err != nil || res.ExitCode != 0 {
		return set
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if db := strings.TrimSpace(line); db != "" {
			set[db] = true
		}
	}
	return set
}

// loadedDB picks the database the dump populated. A dump may CREATE its own
// database, so prefer one that appeared only after the load (diffed against
// before). Falls back to postgres — where loadDump targets — so a database the
// image pre-created (POSTGRES_DB) but the dump never wrote is not mistaken for
// the restored data.
func loadedDB(ctx context.Context, sb sandbox.Sandbox, before map[string]bool) string {
	var created []string
	for db := range listDatabases(ctx, sb) {
		if !before[db] && db != "postgres" {
			created = append(created, db)
		}
	}
	if len(created) > 0 {
		sort.Strings(created)
		return created[0]
	}
	return "postgres"
}

func versionTrap(dumpMajor, imageMajor int) bool {
	return dumpMajor > 0 && imageMajor > 0 && dumpMajor > imageMajor
}

func versionTrapResult(res StepResult, dumpMajor, imageMajor int, dumpSrc string, red *redact.Redactor) StepResult {
	ev := checks.Evidence{
		Kind:     "load",
		Target:   filepath.Base(dumpSrc),
		Expected: fmt.Sprintf("loadable into postgres %d", imageMajor),
		Actual:   fmt.Sprintf("dump is from postgres %d — version trap", dumpMajor),
		Status:   checks.Fail,
	}
	redactEvidence(red, &ev)
	res.Evidence = append(res.Evidence, ev)
	res.Status = checks.Fail
	res.Summary = red.Redact("version trap: " + ev.Actual)
	return res
}

// stageDump decompresses (or copies) the source dump into scratch — bounded by
// the scratch byte quota — and reports its pg_dump format (custom vs plain).
func stageDump(src, scratchRoot string, maxBytes int64) (string, string, error) {
	loaded := filepath.Join(scratchRoot, "dump")
	if err := decompressTo(src, loaded, maxBytes); err != nil {
		return "", "", fmt.Errorf("stage dump %s: %w", filepath.Base(src), err)
	}
	format, err := dumpFormat(loaded)
	if err != nil {
		return "", "", err
	}
	return loaded, format, nil
}

func decompressTo(src, dst string, maxBytes int64) error {
	in, err := os.Open(src) //nolint:gosec // G304: dump path is operator-configured / internal scratch
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	magic := make([]byte, 4)
	n, _ := io.ReadFull(in, magic)
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return err
	}
	out, err := os.Create(dst) //nolint:gosec // G304: dst is internal scratch
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	w := quotaWriter(out, maxBytes) // bound the decompressed output by the scratch quota

	switch {
	case n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		zr, err := gzip.NewReader(in)
		if err != nil {
			return err
		}
		defer func() { _ = zr.Close() }()
		_, err = io.Copy(w, zr) //nolint:gosec // G110: decompressed output is bounded by quotaWriter (the scratch byte quota)
		return err
	case n >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		zr, err := zstd.NewReader(in)
		if err != nil {
			return err
		}
		defer zr.Close()
		_, err = io.Copy(w, zr)
		return err
	default:
		_, err = io.Copy(w, in)
		return err
	}
}

// errScratchQuota marks a stage that would exceed the scratch byte quota. The
// orchestrator records it as error (the auditor declined to run), never fail —
// the backup is not the problem (DESIGN §9.6).
var errScratchQuota = errors.New("scratch quota exceeded")

// quotaWriter bounds w to maxBytes (0 = unbounded), so decompressing a dump that
// expands past the scratch quota fails with errScratchQuota instead of filling
// the disk — the L3 counterpart to the L2 preflight.
func quotaWriter(w io.Writer, maxBytes int64) io.Writer {
	if maxBytes <= 0 {
		return w
	}
	return &limitedWriter{w: w, remaining: maxBytes}
}

type limitedWriter struct {
	w         io.Writer
	remaining int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.remaining {
		n, _ := l.w.Write(p[:l.remaining])
		l.remaining -= int64(n)
		return n, errScratchQuota
	}
	n, err := l.w.Write(p)
	l.remaining -= int64(n)
	return n, err
}

func dumpFormat(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: dst is internal scratch
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 5)
	if n, _ := io.ReadFull(f, head); n >= 5 && string(head[:5]) == "PGDMP" {
		return "custom", nil
	}
	return "plain", nil
}

var (
	tagMajorRe = regexp.MustCompile(`^(\d+)`)
	versionRe  = regexp.MustCompile(`(?i)dumped from database version (\d+)`)
)

// pgMajor extracts the postgres major from an image reference's tag — e.g.
// "postgres:16" or "registry.example.com:5000/library/postgres:16.2" → 16. A
// digest pin or a non-numeric tag (":latest") yields 0, disabling the version
// trap (we can't tell the major). Crucially it never mistakes a registry port
// (host:5000/…) for the tag.
func pgMajor(image string) int {
	ref := image
	if i := strings.IndexByte(ref, '@'); i >= 0 { // drop a digest: "…@sha256:…"
		ref = ref[:i]
	}
	if i := strings.LastIndexByte(ref, '/'); i >= 0 { // drop the registry/path: "host:5000/…"
		ref = ref[i+1:]
	}
	i := strings.LastIndexByte(ref, ':')
	if i < 0 {
		return 0 // no tag
	}
	m := tagMajorRe.FindStringSubmatch(ref[i+1:])
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func plainDumpMajor(path string) int {
	f, err := os.Open(path) //nolint:gosec // G304: dump is internal scratch
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for i := 0; i < 300 && sc.Scan(); i++ { // the version header sits near the top
		if m := versionRe.FindStringSubmatch(sc.Text()); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return 0
}

func customDumpMajor(ctx context.Context, sb sandbox.Sandbox) int {
	res, err := sb.Exec(ctx, []string{"pg_restore", "-l", containerDumpPath})
	if err != nil || res.ExitCode != 0 {
		return 0
	}
	if m := versionRe.FindStringSubmatch(res.Stdout); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func countErrorLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(strings.ToLower(line), "error") {
			n++
		}
	}
	return n
}
