package exec

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// Magic/format detection and decompression must never panic on arbitrary dump
// bytes; an accepted dump reports a known format and its staged output respects
// the scratch quota (a decompression bomb cannot exceed it).
func FuzzStageDump(f *testing.F) {
	f.Add(gzipOf("SELECT 1;"))
	f.Add(zstdOf("SELECT 1;"))
	f.Add([]byte("SELECT 1; -- plain dump\n"))
	f.Add(append([]byte("PGDMP"), 0, 0, 0, 0))
	f.Add(gzipOf(strings.Repeat("A", 4<<20))) // decompression bomb vs the quota
	f.Add([]byte{0x1f, 0x8b, 0x08})           // gzip magic, truncated
	f.Add([]byte{})

	const quota = 1 << 20
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		src := filepath.Join(dir, "in")
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, format, err := stageDump(src, dir, quota)
		if err != nil {
			return // rejection is fine; the point is no panic / no unbounded write
		}
		if format != "plain" && format != "custom" {
			t.Fatalf("stageDump format = %q, want plain or custom", format)
		}
		info, serr := os.Stat(loaded)
		if serr != nil {
			t.Fatalf("staged file missing: %v", serr)
		}
		if info.Size() > quota {
			t.Fatalf("staged output %d exceeds quota %d", info.Size(), quota)
		}
	})
}

func gzipOf(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func zstdOf(s string) []byte {
	var b bytes.Buffer
	w, _ := zstd.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}
