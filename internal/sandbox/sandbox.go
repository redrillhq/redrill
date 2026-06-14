package sandbox

import (
	"context"
	"errors"
)

// ErrNoRuntime means no container runtime is available (e.g. no Docker daemon).
// L3 maps this to skipped, never to pass.
var ErrNoRuntime = errors.New("no sandbox runtime available")

// RunLabel keys sandbox containers to their run so the janitor can reap orphans.
const RunLabel = "io.redrill.run"

type SandboxSpec struct {
	Image    string
	Env      map[string]string
	Network  string            // "none" by default
	Memory   int64             // bytes; 0 = the daemon default
	Labels   map[string]string // includes io.redrill.run=<run_id> for the janitor
	ReadyCmd []string          // polled via Exec until it exits 0 (e.g. pg_isready)
	Files    []FileInject      // copied into the container after it starts
}

type FileInject struct {
	HostPath      string
	ContainerPath string // absolute path inside the container
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type SandboxRuntime interface {
	Start(ctx context.Context, spec SandboxSpec) (Sandbox, error)
}

// Sandbox is a running, network-isolated container. Close must be idempotent.
type Sandbox interface {
	Endpoint(service string) (string, error)
	Exec(ctx context.Context, cmd []string) (ExecResult, error)
	Close(ctx context.Context) error
}
