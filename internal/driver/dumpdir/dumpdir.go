// Package dumpdir reads a directory of dump files (e.g. pg_dump output) as a
// source. It is read-only on the directory: it lists and copies files out, and
// has no path that writes, prunes, or deletes anything in the source.
package dumpdir

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/alyamovsky/drillbit/internal/driver"
)

// Driver lists and restores dump files matching a glob pattern in a directory.
type Driver struct {
	dir     string
	pattern string
}

// New returns a dumpdir driver for files matching pattern (a filepath.Match
// glob, e.g. "myapp-*.sql.gz") in dir.
func New(dir, pattern string) *Driver {
	return &Driver{dir: dir, pattern: pattern}
}

func (d *Driver) Name() string { return "dumpdir" }

func (d *Driver) Capabilities() driver.Capabilities {
	return driver.Capabilities{ListSnapshots: true, PartialRestore: true}
}

// Validate confirms the directory is readable and the pattern is well-formed.
func (d *Driver) Validate(ctx context.Context) error {
	if _, err := filepath.Match(d.pattern, ""); err != nil {
		return fmt.Errorf("dumpdir %s: bad pattern %q: %w", d.dir, d.pattern, err)
	}
	if _, err := os.ReadDir(d.dir); err != nil {
		return fmt.Errorf("dumpdir %s: %w", d.dir, err)
	}
	return nil
}

// ListSnapshots returns the matching dump files, newest first (by mtime, then
// name). An unreadable directory is an error, not an empty list.
func (d *Driver) ListSnapshots(ctx context.Context) ([]driver.Snapshot, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, fmt.Errorf("dumpdir %s: %w", d.dir, err)
	}
	var snaps []driver.Snapshot
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ok, err := filepath.Match(d.pattern, e.Name())
		if err != nil {
			return nil, fmt.Errorf("dumpdir %s: bad pattern %q: %w", d.dir, d.pattern, err)
		}
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("dumpdir %s: stat %s: %w", d.dir, e.Name(), err)
		}
		snaps = append(snaps, driver.Snapshot{ID: e.Name(), Time: info.ModTime().UTC(), Size: info.Size()})
	}
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].Time.Equal(snaps[j].Time) {
			return snaps[i].ID > snaps[j].ID
		}
		return snaps[i].Time.After(snaps[j].Time)
	})
	return snaps, nil
}

// NativeCheck is unsupported: a dumpdir is plain files with no engine-native
// integrity check (Capabilities reports NativeCheck=false, so this is never
// called by the orchestrator).
func (d *Driver) NativeCheck(ctx context.Context, _ driver.NativeCheckOpts) (driver.Report, error) {
	return driver.Report{}, errors.New("dumpdir has no native check")
}

// Path returns the absolute path of a dump file by snapshot ID, for checks that
// inspect the file in place (L1 reads files where they are; no restore needed).
func (d *Driver) Path(id string) string { return filepath.Join(d.dir, id) }

// Restore copies the selected dump files into targetDir, read-only on the
// source. (Used by L2 from M7; the source is only ever read.)
func (d *Driver) Restore(ctx context.Context, sel driver.Selection, targetDir string) (driver.RestoreReport, error) {
	var rep driver.RestoreReport
	for _, id := range sel.SnapshotIDs {
		n, err := copyFile(d.Path(id), filepath.Join(targetDir, id))
		if err != nil {
			return rep, fmt.Errorf("dumpdir %s: restore %s: %w", d.dir, id, err)
		}
		rep.Bytes += n
		rep.Files++
	}
	return rep, nil
}

func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src) //nolint:gosec // G304: source path is operator-configured, read-only
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: restore target is an internal scratch path
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}
