// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package subproc is the shared core for CLI-engine drivers: the process
// runner, env assembly, error formatting, and restore measurement. A fix here
// reaches every engine — the 2026-07-03 review's worst driver bug (a ctx kill
// misread as an engine verdict) had to be fixed twice because this layer was
// duplicated.
package subproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/redrillhq/redrill/internal/driver"
)

// Runner runs a command, returning stdout, stderr, and exit code. err is
// non-nil only when the process could not start or the context was canceled;
// a non-zero exit is reported via exitCode so callers map the engine's own
// exit semantics themselves. dir is the working directory ("" = inherit).
type Runner func(ctx context.Context, dir string, env []string, name string, args []string) (stdout, stderr []byte, exitCode int, err error)

// ExecRunner is the default Runner; the exec layer wraps it with nice/ionice
// when an IO policy is configured.
func ExecRunner(ctx context.Context, dir string, env []string, name string, args []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: argv is built by the drivers, not from user input
	cmd.Dir = dir
	cmd.Env = env
	// SIGTERM first so the engine can release its repo lock; kill after WaitDelay.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	// A kill by ctx must surface as the runner's error, never as an engine exit
	// code (a timed-out check would otherwise read as "backup corrupt").
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout.Bytes(), stderr.Bytes(), -1, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), -1, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// Env returns the inherited environment plus extra KEY=VALUE pairs — the one
// place engine env assembly happens.
func Env(extra ...string) []string {
	return append(os.Environ(), extra...)
}

// Output runs the command and returns stdout, treating any non-zero exit as
// an error ("<label>: exit N: <first stderr line>"). Call sites that map the
// engine's exit code themselves (native checks) use the Runner directly.
func Output(ctx context.Context, run Runner, dir string, env []string, name string, args []string, label string) ([]byte, error) {
	stdout, stderr, exit, err := run(ctx, dir, env, name, args)
	if err != nil {
		return stdout, fmt.Errorf("%s: %w", label, err)
	}
	if exit != 0 {
		return stdout, fmt.Errorf("%s: exit %d: %s", label, exit, OneLine(stderr))
	}
	return stdout, nil
}

// OneLine is the first non-empty line of captured output, for error messages.
func OneLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// DirReport measures a finished restore: file count and total bytes.
func DirReport(dir string) (driver.RestoreReport, error) {
	var rep driver.RestoreReport
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		rep.Files++
		rep.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return driver.RestoreReport{}, fmt.Errorf("measure restore dir: %w", err)
	}
	return rep, nil
}
