// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// jpegTree writes filled non-empty .jpg files and empty ones into a fresh dir.
func jpegTree(t *testing.T, filled, empty int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < filled; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("img-%03d.jpg", i)), "jpegbytes")
	}
	for i := 0; i < empty; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("empty-%03d.jpg", i)), "")
	}
	return dir
}

// writeDeep is write with parent directories created.
func writeDeep(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, path, content)
}

func TestMatchGlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, rel string
		want         bool
	}{
		// No slash: base-name match at any depth (gitignore intuition).
		{"*.jpg", "a.jpg", true},
		{"*.jpg", "photos/2026/07/a.jpg", true},
		{"*.jpg", "photos/a.jpeg", false},
		{"config.php", "config/config.php", true},
		// With a slash: restore-relative path match.
		{"photos/*.jpg", "photos/a.jpg", true},
		{"photos/*.jpg", "photos/2026/a.jpg", false},
		{"photos/*.jpg", "other/a.jpg", false},
		// ** spans zero or more directories.
		{"**/*.jpg", "a.jpg", true},
		{"**/*.jpg", "photos/2026/07/a.jpg", true},
		{"photos/**/*.jpg", "photos/a.jpg", true},
		{"photos/**/*.jpg", "photos/2026/07/a.jpg", true},
		{"photos/**/*.jpg", "other/2026/a.jpg", false},
		{"photos/**", "photos/2026/07/a.jpg", true},
		{"photos/**", "other/a.jpg", false},
		// Character classes still work per segment.
		{"img-[0-9][0-9].png", "shots/img-42.png", true},
		{"img-[0-9][0-9].png", "shots/img-4a.png", false},
	}
	for _, tt := range tests {
		got, err := matchGlob(tt.pattern, tt.rel)
		if err != nil {
			t.Errorf("matchGlob(%q, %q): %v", tt.pattern, tt.rel, err)
			continue
		}
		if got != tt.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.rel, got, tt.want)
		}
	}
}

// A malformed segment surfaces as an error (config validation catches this
// earlier; the check must still never mis-verdict on it).
func TestMatchGlobBadPattern(t *testing.T) {
	t.Parallel()
	if _, err := matchGlob("photos/[", "photos/a.jpg"); err == nil {
		t.Error("bad pattern: err = nil, want ErrBadPattern")
	}
}
