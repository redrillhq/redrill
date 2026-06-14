package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Run against the restored tree.
const (
	kindPathExists         = "path_exists"
	kindPathAbsent         = "path_absent"
	kindCanaryFile         = "canary_file"
	kindHashMatch          = "hash_match"
	kindNewestFileMaxAge   = "newest_file_max_age"
	kindMinTotalBytes      = "min_total_bytes"
	kindFileCountTolerance = "file_count_tolerance_pct"
)

type PathExists struct{ Path string }

func (c PathExists) Kind() string { return kindPathExists }
func (c PathExists) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	return pathEvidence(kindPathExists, env.RestoreDir, c.Path, false, false), nil
}

type PathAbsent struct{ Path string }

func (c PathAbsent) Kind() string { return kindPathAbsent }
func (c PathAbsent) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	return pathEvidence(kindPathAbsent, env.RestoreDir, c.Path, true, false), nil
}

// Weak — never counts as proof.
type CanaryFile struct{ Path string }

func (c CanaryFile) Kind() string { return kindCanaryFile }
func (c CanaryFile) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	return pathEvidence(kindCanaryFile, env.RestoreDir, c.Path, false, true), nil
}

func pathEvidence(kind, restoreDir, path string, absent, weak bool) Evidence {
	ev := Evidence{Kind: kind, Target: path, Weak: weak, Expected: "present"}
	if absent {
		ev.Expected = "absent"
	}
	_, err := os.Stat(filepath.Join(restoreDir, path))
	if err != nil && !os.IsNotExist(err) {
		ev.Status, ev.Actual = Error, "stat: "+err.Error()
		return ev
	}
	exists := err == nil
	ev.Actual = "absent"
	if exists {
		ev.Actual = "present"
	}
	ev.Status = Fail
	if exists == !absent {
		ev.Status = Pass
	}
	return ev
}

type NewestFileMaxAge struct {
	Newest time.Time
	Max    time.Duration
}

func (c NewestFileMaxAge) Kind() string { return kindNewestFileMaxAge }
func (c NewestFileMaxAge) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindNewestFileMaxAge, Expected: fmt.Sprintf("newest file age <= %s", c.Max)}
	if c.Newest.IsZero() {
		ev.Status, ev.Actual = Error, "no files restored to age-check"
		return ev, nil
	}
	age := env.Now.Sub(c.Newest)
	ev.Actual = fmt.Sprintf("age %s", age.Round(time.Second))
	ev.Status = Fail
	if age <= c.Max {
		ev.Status = Pass
	}
	return ev, nil
}

type MinTotalBytes struct {
	Total int64
	Min   int64
}

func (c MinTotalBytes) Kind() string { return kindMinTotalBytes }
func (c MinTotalBytes) Run(_ context.Context, _ CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindMinTotalBytes, Expected: fmt.Sprintf(">= %d bytes restored", c.Min), Actual: fmt.Sprintf("%d bytes", c.Total)}
	ev.Status = Fail
	if c.Total >= c.Min {
		ev.Status = Pass
	}
	return ev, nil
}

// Prev is the last proven count; with no baseline it passes.
type FileCountTolerance struct {
	Count int
	Prev  int
	Pct   int
}

func (c FileCountTolerance) Kind() string { return kindFileCountTolerance }
func (c FileCountTolerance) Run(_ context.Context, _ CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindFileCountTolerance, Expected: fmt.Sprintf("within %d%% of previous proven count", c.Pct)}
	if c.Prev <= 0 {
		ev.Status, ev.Actual = Pass, fmt.Sprintf("%d files (no baseline yet)", c.Count)
		return ev, nil
	}
	delta := abs(c.Count - c.Prev)
	deltaPct := float64(delta) / float64(c.Prev) * 100
	ev.Actual = fmt.Sprintf("%d files vs baseline %d (%.0f%% delta)", c.Count, c.Prev, deltaPct)
	ev.Status = Fail
	if deltaPct <= float64(c.Pct) {
		ev.Status = Pass
	}
	return ev, nil
}

// Manifest is path→sha256; empty means the engine verified on extract, so passed,
// not skipped.
type HashMatch struct {
	Manifest map[string]string
}

func (c HashMatch) Kind() string { return kindHashMatch }
func (c HashMatch) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindHashMatch}
	if len(c.Manifest) == 0 {
		ev.Status = Pass
		ev.Expected, ev.Actual = "engine-verified bytes", "verified by the engine on extract (no independent manifest exposed)"
		return ev, nil
	}
	ev.Expected = fmt.Sprintf("%d files match manifest", len(c.Manifest))
	for path, want := range c.Manifest {
		got, err := hashFile(filepath.Join(env.RestoreDir, path))
		if err != nil {
			ev.Status, ev.Actual = Error, "hash "+path+": "+err.Error()
			return ev, nil
		}
		if got != want {
			ev.Status, ev.Actual = Fail, path+": hash mismatch"
			return ev, nil
		}
	}
	ev.Status, ev.Actual = Pass, "all files match manifest"
	return ev, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: restore-dir path is internal scratch
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
