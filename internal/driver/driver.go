package driver

import (
	"context"
	"time"
)

// Capabilities advertises what a driver's engine can do, so the orchestrator
// only asks for operations the engine supports (DESIGN §9.2).
type Capabilities struct {
	NativeCheck    bool // has an engine-native integrity check (borg check, restic check)
	ListSnapshots  bool
	PartialRestore bool // can restore a subset (sample) rather than the whole repo
	HashManifest   bool // exposes per-file hashes for hash_match
}

// Snapshot is one restorable point: a borg/restic archive, or a dump file in a
// dumpdir. Fields grow per driver/milestone; these are the common ones.
type Snapshot struct {
	ID   string    // archive name, or dump filename
	Time time.Time // archive time / file mtime, UTC
	Size int64     // bytes, best-effort (0 if unknown)
}

// FileEntry is one entry inside a snapshot's contents (e.g. a borg archive
// listing), used to select an L2 restore sample.
type FileEntry struct {
	Path   string
	Size   int64
	Mtime  time.Time
	IsFile bool // a regular file (not a directory or symlink)
}

// NativeCheckOpts parameterizes a native integrity check. Empty for now; the
// borg driver (M6) adds repo/archive scope and read-data options.
type NativeCheckOpts struct{}

// Report is the outcome of a native integrity check (L1 delegation).
type Report struct {
	OK      bool
	Summary string
}

// Selection names what to restore: which snapshots, and (for archive engines
// like borg) which paths inside them. The L2 work (M7) extends this with sample
// sizing (N random + M newest).
type Selection struct {
	SnapshotIDs []string
	Paths       []string // subset to extract; empty means the whole snapshot
}

// RestoreReport summarizes a restore.
type RestoreReport struct {
	Bytes int64
	Files int
}

// SourceDriver reads one kind of backup repository. Signatures are normative
// (DESIGN §9.2). Drivers are read-only on repositories by construction: there
// is deliberately no write/prune/delete method here, and implementations must
// never invoke a repo-mutating engine command.
type SourceDriver interface {
	Name() string
	Capabilities() Capabilities
	Validate(ctx context.Context) error
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
	NativeCheck(ctx context.Context, opts NativeCheckOpts) (Report, error)
	Restore(ctx context.Context, sel Selection, targetDir string) (RestoreReport, error)
}
