// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package driver

import (
	"context"
	"time"
)

type Capabilities struct {
	NativeCheck    bool // engine-native integrity check (borg check, restic check)
	ListSnapshots  bool
	PartialRestore bool // can restore a subset rather than the whole repo
	HashManifest   bool // exposes per-file hashes for hash_match
}

// Snapshot is one restorable point: an archive, or a dump file.
type Snapshot struct {
	ID   string    // archive name, or dump filename
	Time time.Time // archive time / file mtime, UTC
	Size int64     // bytes, best-effort (0 if unknown)
}

// FileEntry is one entry inside a snapshot's contents, used to select a sample.
type FileEntry struct {
	Path   string
	Size   int64
	Mtime  time.Time
	IsFile bool // regular file (not a directory or symlink)
}

type NativeCheckOpts struct {
	// ReadDataSubsetPct (restic only): read this percentage of pack data
	// during the check, catching in-pack bit rot the structural check cannot
	// see. 0 = structural check only.
	ReadDataSubsetPct int
}

type Report struct {
	OK      bool
	Summary string
}

type Selection struct {
	SnapshotIDs []string
	Paths       []string // subset to extract; empty means the whole snapshot
}

type RestoreReport struct {
	Bytes int64
	Files int
}

type SourceDriver interface {
	Name() string
	Capabilities() Capabilities
	Validate(ctx context.Context) error
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
	NativeCheck(ctx context.Context, opts NativeCheckOpts) (Report, error)
	Restore(ctx context.Context, sel Selection, targetDir string) (RestoreReport, error)
}

// Mounter is the optional read-only FUSE seam (restore.mode: mount): engines
// that can present a snapshot as a filesystem implement it, so L2 checks run
// against the mount instead of a full copy into scratch. Mounting never
// writes to the repository.
type Mounter interface {
	// Mount presents snapshotID under mountpoint and blocks until it serves.
	Mount(ctx context.Context, snapshotID, mountpoint string) (MountHandle, error)
}

// MountHandle is a live mount; Unmount must be called (idempotent) when the
// level's checks finish.
type MountHandle interface {
	// Root is the directory holding the snapshot's tree — the mount-mode
	// equivalent of a copy restore's target dir (engine path quirks, like
	// restic's ids/<short>/<absolute path> nesting, are resolved here).
	Root() string
	Unmount() error
}
