// Package fixtures builds deterministic backup fixtures for tests — dumpdirs and
// borg repositories declared as data rather than assembled by shell. It is the
// single source of truth for the gating fixtures.
//
// Test-only: it imports testing and is never linked into the binary (nothing
// under cmd/ or production internal/ imports it).
package fixtures

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Epoch is the fixed UTC clock fixtures default to, so mtimes and ages are
// reproducible regardless of when the test runs.
var Epoch = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

type dumpSpec struct {
	name  string
	body  string
	mtime time.Time
	raw   []byte
	isRaw bool
}

type DumpOption func(*dumpSpec)

// DumpName sets the dump filename; its extension selects compression (.gz/.zst,
// else plain).
func DumpName(name string) DumpOption { return func(s *dumpSpec) { s.name = name } }

// DumpBody sets the uncompressed dump contents.
func DumpBody(body string) DumpOption { return func(s *dumpSpec) { s.body = body } }

// DumpMTime sets the dump's modification time.
func DumpMTime(t time.Time) DumpOption { return func(s *dumpSpec) { s.mtime = t } }

// DumpAge sets the mtime to Epoch minus d (a dump d old at Epoch).
func DumpAge(d time.Duration) DumpOption { return func(s *dumpSpec) { s.mtime = Epoch.Add(-d) } }

// DumpRaw writes bytes verbatim with no compression — for empty, garbage, or
// otherwise hand-crafted corrupt fixtures. Nil writes a 0-byte file.
func DumpRaw(b []byte) DumpOption {
	return func(s *dumpSpec) {
		s.raw = b
		s.isRaw = true
	}
}

// Dump writes one dump file into a fresh dumpdir and returns the directory.
func Dump(t *testing.T, opts ...DumpOption) string {
	t.Helper()
	s := dumpSpec{name: "app-1.sql.gz", body: "SELECT 1;\n", mtime: Epoch}
	for _, o := range opts {
		o(&s)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, s.name)
	if s.isRaw {
		writeBytes(t, p, s.raw)
	} else {
		writeCompressed(t, p, s.body)
	}
	if err := os.Chtimes(p, s.mtime, s.mtime); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeCompressed encodes body by the path extension: gzip, zstd, or plain.
func writeCompressed(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	var w io.WriteCloser
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gz", ".gzip":
		w = gzip.NewWriter(f)
	case ".zst", ".zstd":
		zw, err := zstd.NewWriter(f)
		if err != nil {
			t.Fatal(err)
		}
		w = zw
	default:
		if _, err := io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// GzipBytes returns body gzip-compressed, for building hand-mutated corrupt
// fixtures (truncate or flip a byte, then feed the result to DumpRaw).
func GzipBytes(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ZstdBytes returns body zstd-compressed.
func ZstdBytes(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
