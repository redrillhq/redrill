package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

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

// preflight refusal is error (the auditor declined), never fail.
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

func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	// Statfs_t.Bsize is signed on Linux, unsigned on darwin; the int64 cast is safe on both.
	return availableBytes(st.Bavail, int64(st.Bsize)), nil
}

func availableBytes(blocks uint64, blockSize int64) uint64 {
	if blockSize <= 0 {
		return 0
	}
	return blocks * uint64(blockSize)
}
