package checks

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// L1 dump-file check kinds (DESIGN §6, §7). Each inspects a single dump file in
// place — no restore. A bad backup (too small, corrupt, stale) is Fail; an
// unreadable file is Error (the auditor couldn't check), never conflated.

const (
	kindFileMinBytes    = "file_min_bytes"
	kindCompressionTest = "compression_test"
	kindMaxAge          = "max_age"
)

// FileMinBytes fails if the dump file is smaller than Min bytes (catching empty
// or truncated dumps).
type FileMinBytes struct {
	Path string
	Min  int64
}

func (c FileMinBytes) Kind() string { return kindFileMinBytes }

func (c FileMinBytes) Run(_ context.Context, _ CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindFileMinBytes, Target: filepath.Base(c.Path), Expected: fmt.Sprintf(">= %d bytes", c.Min)}
	info, err := os.Stat(c.Path)
	if err != nil {
		ev.Status, ev.Actual = Error, "stat: "+err.Error()
		return ev, nil
	}
	ev.Actual = fmt.Sprintf("%d bytes", info.Size())
	ev.Status = Fail
	if info.Size() >= c.Min {
		ev.Status = Pass
	}
	return ev, nil
}

// CompressionTest decompresses the whole dump to verify integrity, choosing
// gzip or zstd by extension (DESIGN §6). A decompression failure is Fail (the
// dump is corrupt); an unreadable file or an unrecognized extension is Error.
type CompressionTest struct {
	Path string
}

func (c CompressionTest) Kind() string { return kindCompressionTest }

func (c CompressionTest) Run(_ context.Context, _ CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindCompressionTest, Target: filepath.Base(c.Path)}
	f, err := os.Open(c.Path)
	if err != nil {
		ev.Status, ev.Expected, ev.Actual = Error, "readable", "open: "+err.Error()
		return ev, nil
	}
	defer func() { _ = f.Close() }()

	switch ext := strings.ToLower(filepath.Ext(c.Path)); ext {
	case ".gz", ".gzip":
		ev.Expected = "valid gzip"
		setCompressionResult(&ev, testGzip(f))
	case ".zst", ".zstd":
		ev.Expected = "valid zstd"
		setCompressionResult(&ev, testZstd(f))
	default:
		ev.Status, ev.Expected, ev.Actual = Error, "gzip or zstd by extension", "unrecognized extension "+ext
	}
	return ev, nil
}

func setCompressionResult(ev *Evidence, err error) {
	if err != nil {
		ev.Status, ev.Actual = Fail, err.Error()
		return
	}
	ev.Status, ev.Actual = Pass, "ok"
}

func testGzip(r io.Reader) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	//nolint:gosec // G110: integrity verification must read the whole stream (gzip's CRC/length live at the end); output is discarded (streamed, not buffered), and run time is bounded by the drill timeout.
	_, err = io.Copy(io.Discard, zr)
	return err
}

func testZstd(r io.Reader) error {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()
	_, err = io.Copy(io.Discard, zr)
	return err
}

// MaxAge fails if the dump file's mtime is older than Max (catching a stale
// source whose cron stopped producing fresh dumps).
type MaxAge struct {
	Path string
	Max  time.Duration
}

func (c MaxAge) Kind() string { return kindMaxAge }

func (c MaxAge) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindMaxAge, Target: filepath.Base(c.Path), Expected: fmt.Sprintf("age <= %s", c.Max)}
	info, err := os.Stat(c.Path)
	if err != nil {
		ev.Status, ev.Actual = Error, "stat: "+err.Error()
		return ev, nil
	}
	mtime := info.ModTime().UTC()
	age := env.Now.Sub(mtime)
	ev.Actual = fmt.Sprintf("age %s (mtime %s)", age.Round(time.Second), mtime.Format(time.RFC3339))
	ev.Status = Fail
	if age <= c.Max {
		ev.Status = Pass
	}
	return ev, nil
}
