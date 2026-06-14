package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// scratch is a per-run restore directory with a byte quota. Redrill restores
// into it, runs L2/L3 against it, and removes it afterward (cleanup always).
type scratch struct {
	root     string
	maxBytes int64
}

func newScratch(base string, runID int64, maxBytes int64) (*scratch, error) {
	root := filepath.Join(base, fmt.Sprintf("run-%d", runID))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create scratch %s: %w", root, err)
	}
	return &scratch{root: root, maxBytes: maxBytes}, nil
}

// preflight refuses, before any restore starts, if the predicted restore size
// won't fit the quota or the free disk at the scratch location (DESIGN §9.6).
// The refusal is an error (the auditor declined to run), never a fail.
func (s *scratch) preflight(predicted int64) error {
	if predicted < 0 {
		predicted = 0
	}
	if s.maxBytes > 0 && predicted > s.maxBytes {
		return fmt.Errorf("scratch preflight: predicted %d bytes exceeds quota %d", predicted, s.maxBytes)
	}
	free, err := freeBytes(s.root)
	if err != nil {
		return fmt.Errorf("scratch preflight: %w", err)
	}
	if uint64(predicted) > free {
		return fmt.Errorf("scratch preflight: predicted %d bytes exceeds %d free on disk", predicted, free)
	}
	return nil
}

func (s *scratch) cleanup() { _ = os.RemoveAll(s.root) }

// freeBytes returns the space available to a non-root user at path.
func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return st.Bavail * uint64(st.Bsize), nil
}
