// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package subproc

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"sync"
	"syscall"
	"time"
)

// mountReadyTimeout bounds how long a FUSE mount may take to start serving;
// mountStopGrace how long Stop waits before escalating to signals. Vars so
// lifecycle tests can shorten them.
var (
	mountReadyTimeout = 30 * time.Second
	mountStopGrace    = 10 * time.Second
)

// MountProc is a long-lived FUSE subprocess (borg mount --foreground,
// restic mount): started, polled until the mountpoint serves, and stopped by
// unmounting first, then reaping the child.
type MountProc struct {
	cmd     *osexec.Cmd
	unmount func() error // engine-specific unmount command
	done    chan error   // closed by the waiter with the child's exit error
	stop    sync.Once
	stopErr error
}

// StartMount launches the mount command and waits until ready reports true
// (polled) or the child exits or the timeout lapses. On failure the child is
// reaped before returning. unmount is the engine's own unmount step, run
// first during Stop; the child is signaled afterwards as the fallback.
func StartMount(ctx context.Context, dir string, env []string, name string, args []string, ready func() bool, unmount func() error) (*MountProc, error) {
	cmd := osexec.CommandContext(ctx, name, args...) //nolint:gosec // G204: argv is built by the drivers, not from user input
	cmd.Dir = dir
	cmd.Env = env
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s mount: %w", name, err)
	}
	m := &MountProc{cmd: cmd, unmount: unmount, done: make(chan error, 1)}
	go func() { m.done <- cmd.Wait() }()

	deadline := time.Now().Add(mountReadyTimeout)
	for {
		if ready() {
			return m, nil
		}
		select {
		case err := <-m.done:
			return nil, fmt.Errorf("%s mount exited before serving: %v", name, exitDetail(err))
		case <-ctx.Done():
			_ = m.Stop()
			return nil, fmt.Errorf("%s mount: %w", name, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = m.Stop()
			return nil, fmt.Errorf("%s mount: not serving after %s", name, mountReadyTimeout)
		}
	}
}

// Stop unmounts and reaps the child; idempotent. The unmount command is the
// polite path (it lets the FUSE daemon exit on its own); the SIGTERM via
// killing the child is the fallback for a wedged mount.
func (m *MountProc) Stop() error {
	m.stop.Do(func() {
		var uerr error
		if m.unmount != nil {
			uerr = m.unmount()
		}
		select {
		case <-m.done:
			// Child exited after unmount — the clean path.
		case <-time.After(mountStopGrace):
			_ = m.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-m.done:
			case <-time.After(mountStopGrace):
				_ = m.cmd.Process.Kill()
				<-m.done
			}
			if uerr == nil {
				uerr = errors.New("mount child did not exit after unmount; signaled")
			}
		}
		m.stopErr = uerr
	})
	return m.stopErr
}

// DirServing reports whether dir exists and lists at least one entry — the
// readiness probe for a mountpoint.
func DirServing(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// Unmounter returns the platform unmount step for a mountpoint: fusermount3,
// fusermount, or umount — whichever exists first. Bounded on its own, since
// Stop paths carry no context.
func Unmounter(mountpoint string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var lastErr error
		for _, tool := range [][]string{{"fusermount3", "-u"}, {"fusermount", "-u"}, {"umount"}} {
			path, err := osexec.LookPath(tool[0])
			if err != nil {
				continue
			}
			out, err := osexec.CommandContext(ctx, path, append(tool[1:], mountpoint)...).CombinedOutput() //nolint:gosec // fixed tool list, driver-built mountpoint
			if err == nil {
				return nil
			}
			lastErr = fmt.Errorf("%s: %w: %s", tool[0], err, OneLine(out))
		}
		if lastErr == nil {
			lastErr = errors.New("no unmount tool found (fusermount3, fusermount, umount)")
		}
		return lastErr
	}
}

func exitDetail(err error) string {
	if err == nil {
		return "exit 0"
	}
	return err.Error()
}
