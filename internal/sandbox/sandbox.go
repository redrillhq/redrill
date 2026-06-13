// Package sandbox defines the SandboxRuntime/Sandbox contract for L3 — an
// ephemeral, network-isolated container booted from restored data. The docker
// subpackage implements it via the Engine API. Absence of a runtime degrades L3
// to skipped, never to a silent pass (DESIGN §9.5).
package sandbox

import (
	"context"
	"errors"
)

// ErrNoRuntime means no container runtime is available (e.g. no Docker daemon).
// L3 maps this to skipped (no sandbox runtime), never to pass.
var ErrNoRuntime = errors.New("no sandbox runtime available")

// RunLabel keys sandbox containers to the run that created them, so the janitor
// can reap orphans from crashed runs.
const RunLabel = "io.drillbit.run"

// SandboxSpec describes a sandbox to start (DESIGN §9.5). The struct is an
// implementation detail; the interface signatures below are normative (§9.2).
type SandboxSpec struct {
	Image    string
	Env      map[string]string
	Network  string            // "none" by default
	Memory   int64             // bytes; 0 = the daemon default
	Labels   map[string]string // includes io.drillbit.run=<run_id> for the janitor
	ReadyCmd []string          // polled via Exec until it exits 0 (e.g. pg_isready)
	Files    []FileInject      // copied into the container after it starts
}

// FileInject is a host file to copy into the container before it is used.
type FileInject struct {
	HostPath      string
	ContainerPath string // absolute path inside the container
}

// ExecResult is the outcome of running a command in a sandbox.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// SandboxRuntime starts ephemeral sandboxes. Signature normative (DESIGN §9.2).
type SandboxRuntime interface {
	Start(ctx context.Context, spec SandboxSpec) (Sandbox, error)
}

// Sandbox is a running, network-isolated container. Signatures normative
// (DESIGN §9.2). Close must be idempotent — the startup janitor backs it up.
type Sandbox interface {
	Endpoint(service string) (string, error)
	Exec(ctx context.Context, cmd []string) (ExecResult, error)
	Close(ctx context.Context) error
}
