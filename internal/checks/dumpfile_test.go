package checks

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

var now = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func makeGzip(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func makeZstd(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, c Check) Evidence {
	t.Helper()
	ev, err := c.Run(context.Background(), CheckEnv{Now: now})
	if err != nil {
		t.Fatalf("%s.Run: %v", c.Kind(), err)
	}
	if ev.Kind != c.Kind() {
		t.Errorf("evidence Kind = %q, want %q", ev.Kind, c.Kind())
	}
	return ev
}

func TestFileMinBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "dump.sql.gz")
	write(t, p, "0123456789") // 10 bytes

	if ev := run(t, FileMinBytes{Path: p, Min: 10}); ev.Status != Pass {
		t.Errorf("min=10: status %s, want pass (%s)", ev.Status, ev.Actual)
	}
	if ev := run(t, FileMinBytes{Path: p, Min: 11}); ev.Status != Fail {
		t.Errorf("min=11: status %s, want fail", ev.Status)
	}
	if ev := run(t, FileMinBytes{Path: filepath.Join(dir, "missing"), Min: 1}); ev.Status != Error {
		t.Errorf("missing file: status %s, want error", ev.Status)
	}
}

func TestCompressionTestGzip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	good := filepath.Join(dir, "good.sql.gz")
	makeGzip(t, good, "SELECT 1;")
	if ev := run(t, CompressionTest{Path: good}); ev.Status != Pass {
		t.Errorf("valid gzip: status %s, want pass (%s)", ev.Status, ev.Actual)
	}

	garbage := filepath.Join(dir, "garbage.sql.gz")
	write(t, garbage, "this is not gzip at all")
	if ev := run(t, CompressionTest{Path: garbage}); ev.Status != Fail {
		t.Errorf("garbage gzip: status %s, want fail", ev.Status)
	}

	// A truncated-but-valid-header gzip fails on the trailing CRC/length.
	truncated := filepath.Join(dir, "trunc.sql.gz")
	makeGzip(t, truncated, "a reasonably long body to truncate mid-stream")
	body, _ := os.ReadFile(truncated)
	write(t, truncated, string(body[:len(body)-5]))
	if ev := run(t, CompressionTest{Path: truncated}); ev.Status != Fail {
		t.Errorf("truncated gzip: status %s, want fail", ev.Status)
	}

	empty := filepath.Join(dir, "empty.sql.gz")
	write(t, empty, "")
	if ev := run(t, CompressionTest{Path: empty}); ev.Status != Fail {
		t.Errorf("empty .gz: status %s, want fail", ev.Status)
	}

	plain := filepath.Join(dir, "dump.sql")
	write(t, plain, "SELECT 1;")
	if ev := run(t, CompressionTest{Path: plain}); ev.Status != Error {
		t.Errorf("unrecognized extension: status %s, want error", ev.Status)
	}

	if ev := run(t, CompressionTest{Path: filepath.Join(dir, "missing.gz")}); ev.Status != Error {
		t.Errorf("missing file: status %s, want error", ev.Status)
	}
}

func TestCompressionTestZstd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	good := filepath.Join(dir, "good.sql.zst")
	makeZstd(t, good, "SELECT 1;")
	if ev := run(t, CompressionTest{Path: good}); ev.Status != Pass {
		t.Errorf("valid zstd: status %s, want pass (%s)", ev.Status, ev.Actual)
	}

	garbage := filepath.Join(dir, "garbage.sql.zst")
	write(t, garbage, "definitely not a zstd frame")
	if ev := run(t, CompressionTest{Path: garbage}); ev.Status != Fail {
		t.Errorf("garbage zstd: status %s, want fail", ev.Status)
	}
}

func TestMaxAge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "dump.sql.gz")
	write(t, p, "x")

	fresh := now.Add(-1 * time.Hour)
	if err := os.Chtimes(p, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	if ev := run(t, MaxAge{Path: p, Max: 36 * time.Hour}); ev.Status != Pass {
		t.Errorf("fresh: status %s, want pass (%s)", ev.Status, ev.Actual)
	}

	stale := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(p, stale, stale); err != nil {
		t.Fatal(err)
	}
	if ev := run(t, MaxAge{Path: p, Max: 36 * time.Hour}); ev.Status != Fail {
		t.Errorf("stale: status %s, want fail", ev.Status)
	}

	if ev := run(t, MaxAge{Path: filepath.Join(dir, "missing"), Max: time.Hour}); ev.Status != Error {
		t.Errorf("missing file: status %s, want error", ev.Status)
	}
}
