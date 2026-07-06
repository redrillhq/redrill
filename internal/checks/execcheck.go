// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
)

const kindExec = "exec"

// ExecScript is the L2 escape hatch: an operator-authored command run via
// `sh -c` in the restored tree. Exit 0 = pass, non-zero = fail (the script's
// verdict), a process that cannot start or a canceled ctx = error.
type ExecScript struct {
	Command string
}

func (c ExecScript) Kind() string { return kindExec }

func (c ExecScript) Run(ctx context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindExec, Target: c.Command, Expected: "exit 0"}
	cmd := osexec.CommandContext(ctx, "sh", "-c", c.Command) //nolint:gosec // G204: the command is operator-authored config, the trust boundary
	cmd.Dir = env.RestoreDir
	cmd.Env = append(os.Environ(), "REDRILL_RESTORE_DIR="+env.RestoreDir)
	out, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		ev.Status, ev.Actual = Error, "exec: "+ctxErr.Error()
		return ev, nil
	}
	var exitErr *osexec.ExitError
	switch {
	case errors.As(err, &exitErr):
		ev.Status = Fail
		ev.Actual = execActual(exitErr.ExitCode(), string(out))
	case err != nil:
		ev.Status, ev.Actual = Error, "exec: "+err.Error()
	default:
		ev.Status, ev.Actual = Pass, execActual(0, string(out))
	}
	return ev, nil
}

// ExecSandbox is the L3 escape hatch: the command runs via `sh -c` inside the
// booted sandbox, next to the loaded database. Same verdict mapping.
type ExecSandbox struct {
	Command string
}

func (c ExecSandbox) Kind() string { return kindExec }

func (c ExecSandbox) Run(ctx context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindExec, Target: c.Command, Expected: "exit 0"}
	res, err := env.Sandbox.Exec(ctx, []string{"sh", "-c", c.Command})
	if err != nil {
		ev.Status, ev.Actual = Error, "exec: "+err.Error()
		return ev, nil
	}
	output := res.Stdout
	if res.ExitCode != 0 && res.Stderr != "" {
		output = res.Stderr
	}
	if res.ExitCode == 0 {
		ev.Status = Pass
	} else {
		ev.Status = Fail
	}
	ev.Actual = execActual(res.ExitCode, output)
	return ev, nil
}

func execActual(exit int, output string) string {
	s := fmt.Sprintf("exit %d", exit)
	if line := firstLine(output); line != "" {
		s += ": " + line
	}
	return s
}
