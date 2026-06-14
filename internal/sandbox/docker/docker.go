// Package docker implements the sandbox runtime via the Docker Engine API (which
// also serves podman's compatible socket). Sandboxes default to network=none and
// carry the io.redrill.run label so the janitor can reap orphans.
package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/alyamovsky/redrill/internal/sandbox"
)

// Runtime is a Docker-backed sandbox.SandboxRuntime.
type Runtime struct {
	cli *client.Client
}

// NewRuntime connects to the local Docker/podman daemon. If the daemon is
// unreachable it returns sandbox.ErrNoRuntime, so the caller degrades L3 to
// skipped rather than failing.
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

// Close releases the Docker client.
func (r *Runtime) Close() error { return r.cli.Close() }

// Start creates, starts, waits for, and seeds a sandbox container.
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

	if err := r.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = sb.Close(ctx)
		return nil, fmt.Errorf("start sandbox: %w", err)
	}
	if len(spec.ReadyCmd) > 0 {
		if err := sb.waitReady(ctx, spec.ReadyCmd); err != nil {
			_ = sb.Close(ctx)
			return nil, fmt.Errorf("sandbox not ready: %w", err)
		}
	}
	for _, f := range spec.Files {
		if err := sb.copyIn(ctx, f); err != nil {
			_ = sb.Close(ctx)
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

// Janitor force-removes every container labeled by redrill (orphans from
// crashed runs). It is safe to call at startup and returns how many it removed.
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

// Endpoint is unavailable under network=none — L3 talks to postgres via Exec.
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

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader); err != nil {
		return sandbox.ExecResult{}, fmt.Errorf("exec read: %w", err)
	}
	insp, err := s.cli.ContainerExecInspect(ctx, ex.ID)
	if err != nil {
		return sandbox.ExecResult{}, fmt.Errorf("exec inspect: %w", err)
	}
	return sandbox.ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: insp.ExitCode}, nil
}

// Close force-removes the container; idempotent (a second call, or a container
// already gone, is a no-op).
func (s *dockerSandbox) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.cli.ContainerRemove(ctx, s.id, container.RemoveOptions{Force: true}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("remove sandbox: %w", err)
	}
	return nil
}

// waitReady polls cmd until it exits 0, the container dies, or the context ends.
// Detecting a dead container matters: a postgres that crashes on boot (tight
// memory limit, bad image, corrupt init) would otherwise be polled until the
// deadline; failing fast turns that into a prompt, clear error.
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
// dead) along with its exit code; ok is false if the container is still
// alive/starting or its state can't be read.
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
	if err := tw.WriteHeader(&tar.Header{Name: filepath.Base(f.ContainerPath), Mode: 0o600, Size: int64(len(data))}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return s.cli.CopyToContainer(ctx, s.id, filepath.Dir(f.ContainerPath), &buf, container.CopyToContainerOptions{})
}
