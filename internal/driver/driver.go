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

type NativeCheckOpts struct{}

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
