// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

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

// Inspected in place, no restore.
const (
	kindFileMinBytes    = "file_min_bytes"
	kindCompressionTest = "compression_test"
	kindMaxAge          = "max_age"
)

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

// Decompression failure is Fail; unreadable file or unknown extension is Error.
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

// Won't-decompress is fail; can't-read-disk is error.
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

// Records the last underlying read error so a disk failure can be told apart
// from a decompression failure on bytes that read fine.
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

// The bool reports whether the error came from the underlying reader (I/O), not the data.
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
