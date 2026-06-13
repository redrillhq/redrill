// Package borg reads a BorgBackup repository by invoking the borg CLI (1.x).
// It is read-only on the repository by construction: every borg invocation uses
// a read-only subcommand (list, info, check, extract) — there is no path here
// that runs create, delete, prune, compact, init, or any other repo-mutating
// command, and a test asserts this across the constructed argv.
package borg

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
	"strings"
	"time"

	"github.com/alyamovsky/drillbit/internal/driver"
)

// Runner runs a command and returns its stdout, stderr, and exit code. err is
// non-nil only when the process could not be started (e.g. binary not found) or
// the context was canceled — a non-zero exit is reported via exitCode, not err,
// so callers can tell borg's "errors found" (check exit 1) from an operational
// failure (exit ≥2). dir, when set, is the working directory (borg extract
// writes to it).
type Runner func(ctx context.Context, dir string, env []string, name string, args []string) (stdout, stderr []byte, exitCode int, err error)

// Driver reads a borg repository at repo via the borg binary.
type Driver struct {
	repo       string
	binary     string
	passphrase string
	sshKey     string
	run        Runner
}

// Option configures a Driver.
type Option func(*Driver)

// WithBinary overrides the borg binary (config `binary:` / version pin).
func WithBinary(b string) Option {
	return func(d *Driver) {
		if b != "" {
			d.binary = b
		}
	}
}

// WithPassphrase sets the repository passphrase (already resolved from a
// *_file/*_env ref by the caller). It is passed to borg via BORG_PASSPHRASE,
// never on the command line.
func WithPassphrase(p string) Option { return func(d *Driver) { d.passphrase = p } }

// WithSSHKey sets the SSH private-key path for ssh:// repos (via BORG_RSH).
func WithSSHKey(k string) Option { return func(d *Driver) { d.sshKey = k } }

// WithRunner injects a Runner (tests). nil keeps the default exec runner.
func WithRunner(r Runner) Option {
	return func(d *Driver) {
		if r != nil {
			d.run = r
		}
	}
}

// New returns a borg Driver for repo.
func New(repo string, opts ...Option) *Driver {
	d := &Driver{repo: repo, binary: "borg", run: execRunner}
	for _, o := range opts {
		o(d)
	}
	return d
}

func (d *Driver) Name() string { return "borg" }

func (d *Driver) Capabilities() driver.Capabilities {
	return driver.Capabilities{NativeCheck: true, ListSnapshots: true, PartialRestore: true}
}

// env builds the borg environment: the inherited environment plus the secret
// refs (appended last so they win). The passphrase value never appears on argv.
func (d *Driver) env() []string {
	env := os.Environ()
	if d.passphrase != "" {
		env = append(env, "BORG_PASSPHRASE="+d.passphrase)
	}
	if d.sshKey != "" {
		env = append(env, "BORG_RSH=ssh -i "+d.sshKey+" -o BatchMode=yes")
	}
	return env
}

// Validate confirms the repo is reachable and the passphrase/key work, via a
// cheap read-only `borg list --short`.
func (d *Driver) Validate(ctx context.Context) error {
	_, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, []string{"list", "--short", d.repo})
	if err != nil {
		return fmt.Errorf("borg list %s: %w", d.repo, err)
	}
	if exit != 0 {
		return fmt.Errorf("borg list %s: exit %d: %s", d.repo, exit, oneLine(stderr))
	}
	return nil
}

// ListSnapshots returns the repo's archives, newest first. Borg timestamps are
// naive local time (borg 1.x records no zone); they are read in the host's local
// location, which is correct for the common single-timezone setup.
func (d *Driver) ListSnapshots(ctx context.Context) ([]driver.Snapshot, error) {
	stdout, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, []string{"list", "--json", d.repo})
	if err != nil {
		return nil, fmt.Errorf("borg list %s: %w", d.repo, err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("borg list %s: exit %d: %s", d.repo, exit, oneLine(stderr))
	}
	return parseList(stdout)
}

// NativeCheck delegates L1 integrity to `borg check`. Exit 0 is a clean repo;
// exit 1 means borg found errors (the backup is corrupt — a failing Report);
// exit ≥2 is operational (the auditor couldn't check — an error).
func (d *Driver) NativeCheck(ctx context.Context, _ driver.NativeCheckOpts) (driver.Report, error) {
	_, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, []string{"check", d.repo})
	if err != nil {
		return driver.Report{}, fmt.Errorf("borg check %s: %w", d.repo, err)
	}
	switch exit {
	case 0:
		return driver.Report{OK: true, Summary: "borg check passed"}, nil
	case 1:
		return driver.Report{OK: false, Summary: oneLine(stderr)}, nil
	default:
		return driver.Report{}, fmt.Errorf("borg check %s: exit %d: %s", d.repo, exit, oneLine(stderr))
	}
}

// Restore extracts the selected archive (and optionally a subset of paths) into
// targetDir. Read-only on the repo: `borg extract` only reads.
func (d *Driver) Restore(ctx context.Context, sel driver.Selection, targetDir string) (driver.RestoreReport, error) {
	if len(sel.SnapshotIDs) == 0 {
		return driver.RestoreReport{}, errors.New("borg restore: no archive selected")
	}
	args := []string{"extract", d.repo + "::" + sel.SnapshotIDs[0]}
	if len(sel.Paths) > 0 {
		args = append(args, "--")
		args = append(args, sel.Paths...)
	}
	_, stderr, exit, err := d.run(ctx, targetDir, d.env(), d.binary, args)
	if err != nil {
		return driver.RestoreReport{}, fmt.Errorf("borg extract %s: %w", d.repo, err)
	}
	if exit != 0 {
		return driver.RestoreReport{}, fmt.Errorf("borg extract %s: exit %d: %s", d.repo, exit, oneLine(stderr))
	}
	return dirReport(targetDir)
}

// ArchiveSize returns an archive's original (uncompressed) size via
// `borg info --json`, for size-anomaly detection.
func (d *Driver) ArchiveSize(ctx context.Context, id string) (int64, error) {
	stdout, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, []string{"info", "--json", d.repo + "::" + id})
	if err != nil {
		return 0, fmt.Errorf("borg info %s::%s: %w", d.repo, id, err)
	}
	if exit != 0 {
		return 0, fmt.Errorf("borg info %s::%s: exit %d: %s", d.repo, id, exit, oneLine(stderr))
	}
	return parseArchiveSize(stdout)
}

// --- parsing (against real borg 1.4 --json output; see testdata) ---

type listJSON struct {
	Archives []struct {
		Name string `json:"name"`
		Time string `json:"time"`
	} `json:"archives"`
}

func parseList(b []byte) ([]driver.Snapshot, error) {
	var lj listJSON
	if err := json.Unmarshal(b, &lj); err != nil {
		return nil, fmt.Errorf("parse borg list json: %w", err)
	}
	snaps := make([]driver.Snapshot, 0, len(lj.Archives))
	for _, a := range lj.Archives {
		t, err := parseBorgTime(a.Time)
		if err != nil {
			return nil, fmt.Errorf("archive %q: %w", a.Name, err)
		}
		snaps = append(snaps, driver.Snapshot{ID: a.Name, Time: t})
	}
	// Borg lists oldest→newest; return newest first for "pick newest".
	for i, j := 0, len(snaps)-1; i < j; i, j = i+1, j-1 {
		snaps[i], snaps[j] = snaps[j], snaps[i]
	}
	return snaps, nil
}

type infoJSON struct {
	Archives []struct {
		Stats struct {
			OriginalSize int64 `json:"original_size"`
		} `json:"stats"`
	} `json:"archives"`
}

func parseArchiveSize(b []byte) (int64, error) {
	var ij infoJSON
	if err := json.Unmarshal(b, &ij); err != nil {
		return 0, fmt.Errorf("parse borg info json: %w", err)
	}
	if len(ij.Archives) == 0 {
		return 0, errors.New("borg info json: no archive")
	}
	return ij.Archives[0].Stats.OriginalSize, nil
}

// borgTimeLayouts covers borg 1.x's naive ISO timestamps (microseconds, no zone).
var borgTimeLayouts = []string{
	"2006-01-02T15:04:05.000000",
	"2006-01-02T15:04:05",
}

func parseBorgTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range borgTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparsable borg time %q", s)
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

// execRunner runs the real borg binary. A non-zero exit returns (…, exitCode,
// nil); only a failure to start (or a canceled context) returns a non-nil error.
func execRunner(ctx context.Context, dir string, env []string, name string, args []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name is the configured borg binary; args are read-only borg subcommands built here
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
