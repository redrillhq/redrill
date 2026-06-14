// Package restic reads a restic repository via the restic CLI.
package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alyamovsky/redrill/internal/driver"
)

// Runner runs a command, returning stdout, stderr, and exit code. err is non-nil
// only when the process could not start or the context was canceled; a non-zero
// exit is reported via exitCode so callers can map restic's outcome themselves.
// dir is the working directory.
type Runner func(ctx context.Context, dir string, env []string, name string, args []string) (stdout, stderr []byte, exitCode int, err error)

type Driver struct {
	repo           string
	binary         string
	password       string
	backendEnv     map[string]string // S3/B2 credentials, injected via env, never on argv
	downloadRateKi int64             // restic --limit-download (KiB/s); 0 = unset
	run            Runner
}

type Option func(*Driver)

func WithBinary(b string) Option {
	return func(d *Driver) {
		if b != "" {
			d.binary = b
		}
	}
}

// WithPassword sets the repository password, passed via RESTIC_PASSWORD, never on
// the command line.
func WithPassword(p string) Option { return func(d *Driver) { d.password = p } }

// WithBackendEnv sets backend credentials (e.g. AWS_*, B2_*) injected into the
// environment, never on argv.
func WithBackendEnv(env map[string]string) Option {
	return func(d *Driver) {
		if len(env) > 0 {
			d.backendEnv = env
		}
	}
}

// WithDownloadRateLimit caps restic's transfer rate (KiB/s) via --limit-download;
// 0 leaves it unset.
func WithDownloadRateLimit(kib int64) Option {
	return func(d *Driver) {
		if kib > 0 {
			d.downloadRateKi = kib
		}
	}
}

// WithRunner injects a Runner; nil keeps the default exec runner.
func WithRunner(r Runner) Option {
	return func(d *Driver) {
		if r != nil {
			d.run = r
		}
	}
}

func New(repo string, opts ...Option) *Driver {
	d := &Driver{repo: repo, binary: "restic", run: ExecRunner}
	for _, o := range opts {
		o(d)
	}
	return d
}

func (d *Driver) Name() string { return "restic" }

func (d *Driver) Capabilities() driver.Capabilities {
	return driver.Capabilities{NativeCheck: true, ListSnapshots: true, PartialRestore: true}
}

// env returns the inherited environment plus secret refs; never appears on argv.
func (d *Driver) env() []string {
	env := os.Environ()
	if d.password != "" {
		env = append(env, "RESTIC_PASSWORD="+d.password)
	}
	for k, v := range d.backendEnv {
		env = append(env, k+"="+v)
	}
	return env
}

// global prefixes the repo flag (and an optional rate limit) before a subcommand.
func (d *Driver) global(args ...string) []string {
	out := []string{"-r", d.repo}
	if d.downloadRateKi > 0 {
		out = append(out, "--limit-download", strconv.FormatInt(d.downloadRateKi, 10))
	}
	return append(out, args...)
}

func (d *Driver) Validate(ctx context.Context) error {
	_, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, d.global("snapshots", "--no-lock", "--json"))
	if err != nil {
		return fmt.Errorf("restic snapshots %s: %w", d.repo, err)
	}
	if exit != 0 {
		return fmt.Errorf("restic snapshots %s: exit %d: %s", d.repo, exit, oneLine(stderr))
	}
	return nil
}

// ListSnapshots returns the repo's snapshots, newest first.
func (d *Driver) ListSnapshots(ctx context.Context) ([]driver.Snapshot, error) {
	stdout, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, d.global("snapshots", "--no-lock", "--json"))
	if err != nil {
		return nil, fmt.Errorf("restic snapshots %s: %w", d.repo, err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("restic snapshots %s: exit %d: %s", d.repo, exit, oneLine(stderr))
	}
	return parseSnapshots(stdout)
}

// NativeCheck runs `restic check`. Validate runs first in the L1 flow and proves
// reachability/auth, so a non-zero check here means the repo is bad (a failing
// Report), not that the auditor is blind. A process that can't start is an error.
func (d *Driver) NativeCheck(ctx context.Context, _ driver.NativeCheckOpts) (driver.Report, error) {
	stdout, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, d.global("check"))
	if err != nil {
		return driver.Report{}, fmt.Errorf("restic check %s: %w", d.repo, err)
	}
	if exit == 0 {
		return driver.Report{OK: true, Summary: "restic check passed"}, nil
	}
	summary := oneLine(stderr)
	if summary == "" {
		summary = oneLine(stdout)
	}
	return driver.Report{OK: false, Summary: summary}, nil
}

// ListFiles returns the regular-file entries of a snapshot, with the snapshot's
// single backup root stripped so paths are relative (engine-agnostic, like borg).
func (d *Driver) ListFiles(ctx context.Context, id string) ([]driver.FileEntry, error) {
	stdout, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, d.global("ls", "--no-lock", "--json", id))
	if err != nil {
		return nil, fmt.Errorf("restic ls %s: %w", id, err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("restic ls %s: exit %d: %s", id, exit, oneLine(stderr))
	}
	return parseFiles(stdout)
}

// SnapshotSize returns a snapshot's restore (uncompressed) size.
func (d *Driver) SnapshotSize(ctx context.Context, id string) (int64, error) {
	stdout, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, d.global("stats", "--no-lock", "--json", "--mode", "restore-size", id))
	if err != nil {
		return 0, fmt.Errorf("restic stats %s: %w", id, err)
	}
	if exit != 0 {
		return 0, fmt.Errorf("restic stats %s: exit %d: %s", id, exit, oneLine(stderr))
	}
	return parseStatsSize(stdout)
}

// Restore extracts the selected snapshot (or a subset of paths) into targetDir.
// restic stores absolute paths, so files are restored into a staging dir and the
// snapshot's single backup root is stripped, leaving a relative tree under
// targetDir (matching borg). Selection.Paths are relative to that root.
func (d *Driver) Restore(ctx context.Context, sel driver.Selection, targetDir string) (driver.RestoreReport, error) {
	if len(sel.SnapshotIDs) == 0 {
		return driver.RestoreReport{}, errors.New("restic restore: no snapshot selected")
	}
	id := sel.SnapshotIDs[0]
	root, err := d.snapshotRoot(ctx, id)
	if err != nil {
		return driver.RestoreReport{}, err
	}

	stage := filepath.Join(targetDir, ".restic-stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return driver.RestoreReport{}, fmt.Errorf("restic restore: stage dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	args := d.global("restore", id, "--no-lock", "--target", stage)
	for _, p := range sel.Paths {
		args = append(args, "--include", joinRoot(root, p))
	}
	_, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, args)
	if err != nil {
		return driver.RestoreReport{}, fmt.Errorf("restic restore %s: %w", id, err)
	}
	if exit != 0 {
		return driver.RestoreReport{}, fmt.Errorf("restic restore %s: exit %d: %s", id, exit, oneLine(stderr))
	}

	if err := promote(stage, root, targetDir); err != nil {
		return driver.RestoreReport{}, err
	}
	return dirReport(targetDir)
}

// snapshotRoot returns a snapshot's single backup path (stripped on restore), or
// "" when it has none or several (then the absolute layout is kept).
func (d *Driver) snapshotRoot(ctx context.Context, id string) (string, error) {
	stdout, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, d.global("snapshots", "--no-lock", "--json", id))
	if err != nil {
		return "", fmt.Errorf("restic snapshots %s: %w", id, err)
	}
	if exit != 0 {
		return "", fmt.Errorf("restic snapshots %s: exit %d: %s", id, exit, oneLine(stderr))
	}
	paths, err := parseSnapshotPaths(stdout)
	if err != nil {
		return "", err
	}
	if len(paths) == 1 {
		return paths[0], nil
	}
	return "", nil
}

// promote moves the restored tree out of the staging dir into targetDir, stripping
// root (so a file stored at <root>/x lands at targetDir/x). With no root, the
// staged contents (their absolute nesting) move up unchanged.
func promote(stage, root, targetDir string) error {
	inner := stage
	if root != "" {
		inner = filepath.Join(stage, filepath.FromSlash(strings.TrimPrefix(root, "/")))
	}
	entries, err := os.ReadDir(inner)
	if err != nil {
		return fmt.Errorf("restic restore: read staged %s: %w", inner, err)
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(inner, e.Name()), filepath.Join(targetDir, e.Name())); err != nil {
			return fmt.Errorf("restic restore: promote %s: %w", e.Name(), err)
		}
	}
	return nil
}

// joinRoot makes a stored absolute path from a root and a relative selection path.
func joinRoot(root, rel string) string {
	if root == "" {
		return rel
	}
	return strings.TrimRight(root, "/") + "/" + strings.TrimPrefix(rel, "/")
}

type snapshotJSON struct {
	Time    string   `json:"time"`
	ID      string   `json:"id"`
	ShortID string   `json:"short_id"`
	Paths   []string `json:"paths"`
}

func parseSnapshots(b []byte) ([]driver.Snapshot, error) {
	var sj []snapshotJSON
	if err := json.Unmarshal(b, &sj); err != nil {
		return nil, fmt.Errorf("parse restic snapshots json: %w", err)
	}
	snaps := make([]driver.Snapshot, 0, len(sj))
	for _, s := range sj {
		t, err := parseResticTime(s.Time)
		if err != nil {
			return nil, fmt.Errorf("snapshot %q: %w", s.ID, err)
		}
		snaps = append(snaps, driver.Snapshot{ID: s.ID, Time: t})
	}
	// restic lists oldest-first; sort newest-first.
	sortByTimeDesc(snaps)
	return snaps, nil
}

func parseSnapshotPaths(b []byte) ([]string, error) {
	var sj []snapshotJSON
	if err := json.Unmarshal(b, &sj); err != nil {
		return nil, fmt.Errorf("parse restic snapshots json: %w", err)
	}
	if len(sj) == 0 {
		return nil, nil
	}
	return sj[0].Paths, nil
}

// nodeJSON is one `restic ls --json` line: either the leading snapshot header
// (carries Paths) or a node (carries a type + path). restic 0.17 renamed
// struct_type to message_type, so both are read.
type nodeJSON struct {
	MessageType string   `json:"message_type"`
	StructType  string   `json:"struct_type"`
	Type        string   `json:"type"`
	Path        string   `json:"path"`
	Size        int64    `json:"size"`
	Mtime       string   `json:"mtime"`
	Paths       []string `json:"paths"`
}

func parseFiles(b []byte) ([]driver.FileEntry, error) {
	var root string
	var out []driver.FileEntry
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var n nodeJSON
		if err := json.Unmarshal(line, &n); err != nil {
			return nil, fmt.Errorf("parse restic ls line: %w", err)
		}
		if n.isSnapshot() {
			if len(n.Paths) == 1 {
				root = n.Paths[0]
			}
			continue
		}
		if n.Type == "" || n.Path == "" {
			continue
		}
		fe := driver.FileEntry{Path: stripRoot(root, n.Path), Size: n.Size, IsFile: n.Type == "file"}
		if t, err := parseResticTime(n.Mtime); err == nil {
			fe.Mtime = t
		}
		out = append(out, fe)
	}
	return out, nil
}

func (n nodeJSON) isSnapshot() bool {
	return n.MessageType == "snapshot" || n.StructType == "snapshot" || (n.Type == "" && len(n.Paths) > 0)
}

// stripRoot makes an absolute stored path relative to its backup root.
func stripRoot(root, p string) string {
	if root == "" {
		return strings.TrimPrefix(p, "/")
	}
	rel := strings.TrimPrefix(p, root)
	return strings.TrimPrefix(rel, "/")
}

type statsJSON struct {
	TotalSize int64 `json:"total_size"`
}

func parseStatsSize(b []byte) (int64, error) {
	var sj statsJSON
	if err := json.Unmarshal(b, &sj); err != nil {
		return 0, fmt.Errorf("parse restic stats json: %w", err)
	}
	return sj.TotalSize, nil
}

func parseResticTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("unparsable restic time %q: %w", s, err)
	}
	return t.UTC(), nil
}

func sortByTimeDesc(snaps []driver.Snapshot) {
	for i := 1; i < len(snaps); i++ {
		for j := i; j > 0 && snaps[j].Time.After(snaps[j-1].Time); j-- {
			snaps[j], snaps[j-1] = snaps[j-1], snaps[j]
		}
	}
}

func oneLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func dirReport(dir string) (driver.RestoreReport, error) {
	var rep driver.RestoreReport
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		rep.Files++
		rep.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return driver.RestoreReport{}, fmt.Errorf("measure restore dir: %w", err)
	}
	return rep, nil
}

// ExecRunner is the default Runner; the exec layer wraps it with nice/ionice when
// an IO policy is configured.
func ExecRunner(ctx context.Context, dir string, env []string, name string, args []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: argv is built here, not from user input
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), -1, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}
