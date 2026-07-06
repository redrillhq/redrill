// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package borg reads a BorgBackup repository via the borg CLI (1.x).
package borg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redrillhq/redrill/internal/driver"
	"github.com/redrillhq/redrill/internal/driver/subproc"
)

// Runner is the shared subprocess runner; borg maps its exit semantics itself
// where they carry a verdict (borg check: 1 = errors found, >=2 = operational).
type Runner = subproc.Runner

type Driver struct {
	repo         string
	binary       string
	passphrase   string
	sshKey       string
	uploadRateKi int64 // borg --upload-ratelimit (KiB/s); 0 = unset
	run          Runner
}

type Option func(*Driver)

func WithBinary(b string) Option {
	return func(d *Driver) {
		if b != "" {
			d.binary = b
		}
	}
}

// WithPassphrase sets the repository passphrase, passed via BORG_PASSPHRASE,
// never on the command line.
func WithPassphrase(p string) Option { return func(d *Driver) { d.passphrase = p } }

// WithSSHKey sets the SSH private-key path for ssh:// repos (via BORG_RSH).
func WithSSHKey(k string) Option { return func(d *Driver) { d.sshKey = k } }

// WithUploadRateLimit caps borg's transfer rate (KiB/s) on extract via borg's
// own --upload-ratelimit; 0 leaves it unset. Best-effort: borg throttles the
// repo-side direction it supports.
func WithUploadRateLimit(kib int64) Option {
	return func(d *Driver) {
		if kib > 0 {
			d.uploadRateKi = kib
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

var (
	_ driver.SourceDriver = (*Driver)(nil)
	_ driver.Mounter      = (*Driver)(nil)
)

func New(repo string, opts ...Option) *Driver {
	d := &Driver{repo: repo, binary: "borg", run: subproc.ExecRunner}
	for _, o := range opts {
		o(d)
	}
	return d
}

func (d *Driver) Name() string { return "borg" }

func (d *Driver) Capabilities() driver.Capabilities {
	return driver.Capabilities{NativeCheck: true, ListSnapshots: true, PartialRestore: true}
}

// env returns the environment for borg children: ambient BORG_* is dropped
// (the config is the only sanctioned channel), secrets ride env never argv,
// and the two access acknowledgements are pinned — borg would otherwise
// *prompt* about a relocated or unknown-unencrypted repo, which under a
// captured stdin reads EOF and surfaces as a spurious error. Answering yes
// is safe here: access is read-only by construction.
func (d *Driver) env() []string {
	extra := []string{
		"BORG_RELOCATED_REPO_ACCESS_IS_OK=yes",
		"BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK=yes",
	}
	if d.passphrase != "" {
		extra = append(extra, "BORG_PASSPHRASE="+d.passphrase)
	}
	if d.sshKey != "" {
		extra = append(extra, "BORG_RSH=ssh -i "+d.sshKey+" -o BatchMode=yes")
	}
	return subproc.Env([]string{"BORG_"}, extra...)
}

func (d *Driver) Validate(ctx context.Context) error {
	_, err := subproc.Output(ctx, d.run, "", d.env(), d.binary,
		[]string{"list", "--short", d.repo}, "borg list "+d.repo)
	return err
}

// ListSnapshots returns the repo's archives, newest first. borg 1.x records no
// zone, so timestamps are read as naive local time.
func (d *Driver) ListSnapshots(ctx context.Context) ([]driver.Snapshot, error) {
	stdout, err := subproc.Output(ctx, d.run, "", d.env(), d.binary,
		[]string{"list", "--json", d.repo}, "borg list "+d.repo)
	if err != nil {
		return nil, err
	}
	return parseList(stdout)
}

// NativeCheck runs `borg check`. Exit 0 = clean; exit 1 = errors found (backup
// corrupt, a failing Report); exit >=2 = operational (an error).
func (d *Driver) NativeCheck(ctx context.Context, _ driver.NativeCheckOpts) (driver.Report, error) {
	_, stderr, exit, err := d.run(ctx, "", d.env(), d.binary, []string{"check", d.repo})
	if err != nil {
		return driver.Report{}, fmt.Errorf("borg check %s: %w", d.repo, err)
	}
	switch exit {
	case 0:
		return driver.Report{OK: true, Summary: "borg check passed"}, nil
	case 1:
		return driver.Report{OK: false, Summary: subproc.OneLine(stderr)}, nil
	default:
		return driver.Report{}, fmt.Errorf("borg check %s: exit %d: %s", d.repo, exit, subproc.OneLine(stderr))
	}
}

// Restore extracts the selected archive (or a subset of paths) into targetDir.
func (d *Driver) Restore(ctx context.Context, sel driver.Selection, targetDir string) (driver.RestoreReport, error) {
	if len(sel.SnapshotIDs) == 0 {
		return driver.RestoreReport{}, errors.New("borg restore: no archive selected")
	}
	args := []string{"extract"}
	if d.uploadRateKi > 0 {
		args = append(args, "--upload-ratelimit", strconv.FormatInt(d.uploadRateKi, 10))
	}
	args = append(args, d.repo+"::"+sel.SnapshotIDs[0])
	if len(sel.Paths) > 0 {
		args = append(args, "--")
		args = append(args, sel.Paths...)
	}
	if _, err := subproc.Output(ctx, d.run, targetDir, d.env(), d.binary, args, "borg extract "+d.repo); err != nil {
		return driver.RestoreReport{}, err
	}
	return subproc.DirReport(targetDir)
}

func (d *Driver) ListFiles(ctx context.Context, archive string) ([]driver.FileEntry, error) {
	ref := d.repo + "::" + archive
	stdout, err := subproc.Output(ctx, d.run, "", d.env(), d.binary,
		[]string{"list", "--json-lines", ref}, "borg list "+ref)
	if err != nil {
		return nil, err
	}
	return parseFiles(stdout)
}

// Mount presents an archive read-only via borg's FUSE support
// (restore.mode: mount). The child runs `borg mount --foreground`; readiness
// is the mountpoint serving entries; unmount is `borg umount`, with a signal
// to the child as fallback.
func (d *Driver) Mount(ctx context.Context, snapshotID, mountpoint string) (driver.MountHandle, error) {
	args := []string{"mount", "--foreground", d.repo + "::" + snapshotID, mountpoint}
	proc, err := subproc.StartMount(ctx, "", d.env(), d.binary, args,
		func() bool { return subproc.DirServing(mountpoint) },
		func() error {
			_, err := subproc.Output(ctx, d.run, "", d.env(), d.binary,
				[]string{"umount", mountpoint}, "borg umount "+mountpoint)
			return err
		})
	if err != nil {
		return nil, err
	}
	return borgMount{proc: proc, root: mountpoint}, nil
}

// borgMount serves the archive's stored paths directly at the mountpoint —
// the same tree shape as a borg copy restore.
type borgMount struct {
	proc *subproc.MountProc
	root string
}

func (m borgMount) Root() string   { return m.root }
func (m borgMount) Unmount() error { return m.proc.Stop() }

// ArchiveSize returns an archive's original (uncompressed) size.
func (d *Driver) ArchiveSize(ctx context.Context, id string) (int64, error) {
	ref := d.repo + "::" + id
	stdout, err := subproc.Output(ctx, d.run, "", d.env(), d.binary,
		[]string{"info", "--json", ref}, "borg info "+ref)
	if err != nil {
		return 0, err
	}
	return parseArchiveSize(stdout)
}

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
		// Tolerate an unparsable timestamp (zero time), matching parseFiles:
		// one odd archive must not blind the auditor to the whole repository.
		// A zero time reads as infinitely old, so freshness checks err on the
		// alarming side, never the comforting one.
		t, err := parseBorgTime(a.Time)
		if err != nil {
			t = time.Time{}
		}
		snaps = append(snaps, driver.Snapshot{ID: a.Name, Time: t})
	}
	// Borg lists oldest-first; reverse to newest-first.
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

type fileLineJSON struct {
	Type  string `json:"type"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

func parseFiles(b []byte) ([]driver.FileEntry, error) {
	var out []driver.FileEntry
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var fl fileLineJSON
		if err := json.Unmarshal(line, &fl); err != nil {
			return nil, fmt.Errorf("parse borg list line: %w", err)
		}
		fe := driver.FileEntry{Path: fl.Path, Size: fl.Size, IsFile: fl.Type == "-"}
		if t, err := parseBorgTime(fl.Mtime); err == nil {
			fe.Mtime = t
		}
		out = append(out, fe)
	}
	return out, nil
}

// borg 1.x ISO timestamps: naive, no zone.
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
