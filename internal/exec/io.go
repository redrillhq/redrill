// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"context"
	"strconv"
)

// IOPolicy throttles spawned engine processes.
type IOPolicy struct {
	NiceCPU      int    // niceness; 0 = leave unchanged
	IOClass      string // idle | best-effort | none (none/"" = leave unchanged)
	BandwidthKiB int64  // KiB/s; 0 = unset
}

func (p IOPolicy) wraps() bool { return p.NiceCPU != 0 || ioClassNum(p.IOClass) >= 0 }

// decorate prefixes name+args with nice/ionice so the spawned engine inherits
// the IO discipline. The repo stays read-only; this only re-prioritizes.
func (p IOPolicy) decorate(name string, args []string) (string, []string) {
	var prefix []string
	if p.NiceCPU != 0 {
		prefix = append(prefix, "nice", "-n", strconv.Itoa(p.NiceCPU))
	}
	if c := ioClassNum(p.IOClass); c >= 0 {
		prefix = append(prefix, "ionice", "-c", strconv.Itoa(c))
	}
	if len(prefix) == 0 {
		return name, args
	}
	full := make([]string, 0, len(prefix)+1+len(args))
	full = append(full, prefix...)
	full = append(full, name)
	full = append(full, args...)
	return full[0], full[1:]
}

// ioClassNum maps a config io_class to the ionice class number, or -1 to skip
// ionice (none / unset).
func ioClassNum(class string) int {
	switch class {
	case "idle":
		return 3
	case "best-effort":
		return 2
	default:
		return -1
	}
}

// wrapIO returns a runner that applies nice/ionice; an inert policy returns base
// unchanged so the common (no-policy) path stays a plain exec. Generic over the
// engine Runner types (borg.Runner, restic.Runner) — same underlying signature.
func wrapIO[T ~func(context.Context, string, []string, string, []string) ([]byte, []byte, int, error)](base T, p IOPolicy) T {
	if !p.wraps() {
		return base
	}
	return T(func(ctx context.Context, dir string, env []string, name string, args []string) ([]byte, []byte, int, error) {
		wname, wargs := p.decorate(name, args)
		return base(ctx, dir, env, wname, wargs)
	})
}
