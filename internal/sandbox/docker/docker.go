// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package docker runs sandboxes on the Docker Engine API (and podman's compatible socket).
package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/redrillhq/redrill/internal/sandbox"
)

type Runtime struct {
	cli *client.Client
}

// NewRuntime connects to the local Docker/podman daemon, returning
// sandbox.ErrNoRuntime if unreachable so the caller degrades L3 to skipped.
func NewRuntime(ctx context.Context) (*Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", sandbox.ErrNoRuntime, err)
	}
	if _, err := cli.Ping(ctx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("%w: %w", sandbox.ErrNoRuntime, err)
	}
	return &Runtime{cli: cli}, nil
}

func (r *Runtime) Close() error { return r.cli.Close() }

func (r *Runtime) Start(ctx context.Context, spec sandbox.SandboxSpec) (sandbox.Sandbox, error) {
	if err := r.ensureImage(ctx, spec.Image); err != nil {
		return nil, err
	}
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	netMode := spec.Network
	if netMode == "" {
		netMode = "none"
	}

	created, err := r.cli.ContainerCreate(ctx,
		&container.Config{Image: spec.Image, Env: env, Labels: spec.Labels},
		&container.HostConfig{
			NetworkMode: container.NetworkMode(netMode),
			Resources:   container.Resources{Memory: spec.Memory},
		}, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	sb := &dockerSandbox{cli: r.cli, id: created.ID}
	// Cleanup must succeed even when ctx expiring is why we're bailing out.
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = sb.Close(cctx)
	}

	if err := r.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		cleanup()
		return nil, fmt.Errorf("start sandbox: %w", err)
	}
	if len(spec.ReadyCmd) > 0 {
		if err := sb.waitReady(ctx, spec.ReadyCmd); err != nil {
			cleanup()
			return nil, fmt.Errorf("sandbox not ready: %w", err)
		}
	}
	for _, f := range spec.Files {
		if err := sb.copyIn(ctx, f); err != nil {
			cleanup()
			return nil, fmt.Errorf("copy %s into sandbox: %w", f.HostPath, err)
		}
	}
	return sb, nil
}

func (r *Runtime) ensureImage(ctx context.Context, ref string) error {
	if _, err := r.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	rc, err := r.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	_, _ = io.Copy(io.Discard, rc) // drain so the pull completes
	return nil
}

// Janitor force-removes every container labeled by redrill (orphans from crashed
// runs) and returns how many it removed. Safe to call at startup.
func (r *Runtime) Janitor(ctx context.Context) (int, error) {
	list, err := r.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", sandbox.RunLabel)),
	})
	if err != nil {
		return 0, fmt.Errorf("janitor list: %w", err)
	}
	removed := 0
	for _, c := range list {
		if err := r.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err == nil {
			removed++
		}
	}
	return removed, nil
}

type dockerSandbox struct {
	cli    *client.Client
	id     string
	closed bool
}

// Endpoint is unavailable under network=none; L3 talks to postgres via Exec.
func (s *dockerSandbox) Endpoint(string) (string, error) {
	return "", fmt.Errorf("sandbox endpoints are unavailable under network=none; use Exec")
}

func (s *dockerSandbox) Exec(ctx context.Context, cmd []string) (sandbox.ExecResult, error) {
	ex, err := s.cli.ContainerExecCreate(ctx, s.id, container.ExecOptions{
		Cmd: cmd, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return sandbox.ExecResult{}, fmt.Errorf("exec create: %w", err)
	}
	att, err := s.cli.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		return sandbox.ExecResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()

	// The stream copy only ends when the process exits or the conn closes, so a
	// canceled ctx must close the conn or a hung in-container command blocks the
	// drill past its timeout.
	stop := context.AfterFunc(ctx, func() { att.Close() })
	defer stop()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return sandbox.ExecResult{}, fmt.Errorf("exec read: %w", ctxErr)
		}
		return sandbox.ExecResult{}, fmt.Errorf("exec read: %w", err)
	}
	exit, err := s.execExitCode(ctx, ex.ID)
	if err != nil {
		return sandbox.ExecResult{}, err
	}
	return sandbox.ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exit}, nil
}

// execExitCode polls until the exec has actually finished: right after stream
// EOF the daemon can still report Running with a placeholder ExitCode 0, and a
// racy zero must never feed a check verdict.
func (s *dockerSandbox) execExitCode(ctx context.Context, execID string) (int, error) {
	for {
		insp, err := s.cli.ContainerExecInspect(ctx, execID)
		if err != nil {
			return 0, fmt.Errorf("exec inspect: %w", err)
		}
		if !insp.Running {
			return insp.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("exec inspect: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Close force-removes the container; idempotent (already-gone is a no-op).
// closed is set only on success so a failed removal (e.g. a canceled ctx) can
// be retried instead of silently leaking the container.
func (s *dockerSandbox) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	if err := s.cli.ContainerRemove(ctx, s.id, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove sandbox: %w", err)
	}
	s.closed = true
	return nil
}

// waitReady polls cmd until it exits 0, the container dies, or the context ends.
// Detecting a dead container fails fast instead of polling a crashed boot
// (tight memory, bad image, corrupt init) to the deadline.
func (s *dockerSandbox) waitReady(ctx context.Context, cmd []string) error {
	for {
		if res, err := s.Exec(ctx, cmd); err == nil && res.ExitCode == 0 {
			return nil
		}
		if status, code, ok := s.terminalState(ctx); ok {
			return fmt.Errorf("container exited before ready (status %s, exit %d)", status, code)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// terminalState reports whether the container has stopped for good (exited or
// dead), with its exit code; ok is false if still alive or state is unreadable.
func (s *dockerSandbox) terminalState(ctx context.Context) (status string, exitCode int, ok bool) {
	insp, err := s.cli.ContainerInspect(ctx, s.id)
	if err != nil || insp.State == nil {
		return "", 0, false
	}
	if insp.State.Status == "exited" || insp.State.Status == "dead" {
		return insp.State.Status, insp.State.ExitCode, true
	}
	return "", 0, false
}

func (s *dockerSandbox) copyIn(ctx context.Context, f sandbox.FileInject) error {
	data, err := os.ReadFile(f.HostPath)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Container paths are always slash-separated, hence path, not filepath.
	if err := tw.WriteHeader(&tar.Header{Name: path.Base(f.ContainerPath), Mode: 0o600, Size: int64(len(data))}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return s.cli.CopyToContainer(ctx, s.id, path.Dir(f.ContainerPath), &buf, container.CopyToContainerOptions{})
}
