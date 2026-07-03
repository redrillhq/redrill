// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	free, err := FreeBytes(s.root)
	if err != nil {
		return fmt.Errorf("scratch preflight: %w", err)
	}
	if uint64(predicted) > free {
		return fmt.Errorf("scratch preflight: predicted %d bytes exceeds %d free on disk", predicted, free)
	}
	return nil
}

func (s *scratch) cleanup() { _ = os.RemoveAll(s.root) }

// CleanStaleScratch removes run-* dirs a crashed process left behind; returns
// how many. Startup-only, like the sandbox janitor — it can't tell an orphan
// from another process's live restore.
func CleanStaleScratch(base string) (int, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("clean scratch %s: %w", base, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "run-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(base, e.Name())); err != nil {
			return n, fmt.Errorf("clean scratch %s: %w", base, err)
		}
		n++
	}
	return n, nil
}

// FreeBytes returns the bytes available to an unprivileged writer at path.
func FreeBytes(path string) (uint64, error) {
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
