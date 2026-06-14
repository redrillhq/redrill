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
		cErr, ioFail := testGzip(f)
		setCompressionResult(&ev, cErr, ioFail)
	case ".zst", ".zstd":
		ev.Expected = "valid zstd"
		cErr, ioFail := testZstd(f)
		setCompressionResult(&ev, cErr, ioFail)
	default:
		ev.Status, ev.Expected, ev.Actual = Error, "gzip or zstd by extension", "unrecognized extension "+ext
	}
	return ev, nil
}

// setCompressionResult maps the decompression outcome to a verdict, keeping
// fail≠error: a stream that won't decompress is a corrupt dump (fail), but a
// failure to read the bytes off disk is the auditor's problem (error).
func setCompressionResult(ev *Evidence, err error, ioFailure bool) {
	switch {
	case err == nil:
		ev.Status, ev.Actual = Pass, "ok"
	case ioFailure:
		ev.Status, ev.Actual = Error, "read: "+err.Error()
	default:
		ev.Status, ev.Actual = Fail, err.Error()
	}
}

// ioErrReader records the last underlying read error (other than EOF) so a
// disk/transport failure can be told apart from a genuine decompression failure
// on bytes that read fine.
type ioErrReader struct {
	r   io.Reader
	err error
}

func (e *ioErrReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

// testGzip decompresses the whole stream. It returns the decompression error (if
// any) and whether that error came from the underlying reader (an I/O failure)
// rather than the gzip data itself — so the caller can keep fail≠error.
func testGzip(r io.Reader) (error, bool) {
	er := &ioErrReader{r: r}
	zr, err := gzip.NewReader(er)
	if err != nil {
		return err, er.err != nil
	}
	defer func() { _ = zr.Close() }()
	//nolint:gosec // G110: integrity verification must read the whole stream (gzip's CRC/length live at the end); output is discarded (streamed, not buffered), and run time is bounded by the drill timeout.
	_, err = io.Copy(io.Discard, zr)
	return err, er.err != nil
}

func testZstd(r io.Reader) (error, bool) {
	er := &ioErrReader{r: r}
	zr, err := zstd.NewReader(er)
	if err != nil {
		return err, er.err != nil
	}
	defer zr.Close()
	_, err = io.Copy(io.Discard, zr)
	return err, er.err != nil
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
