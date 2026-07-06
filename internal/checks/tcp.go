// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"context"
	"fmt"
)

const kindTCP = "tcp"

// TCPPort asserts a service inside the sandbox accepts connections on the
// port — the generic "service answers" probe. The sandbox runs with
// network=none, so the probe executes inside it (bash's /dev/tcp against
// loopback); connection refused means the restored data did not produce a
// listening service.
type TCPPort struct {
	Port int
}

func (c TCPPort) Kind() string { return kindTCP }

func (c TCPPort) Run(ctx context.Context, env CheckEnv) (Evidence, error) {
	target := fmt.Sprintf("127.0.0.1:%d", c.Port)
	ev := Evidence{Kind: kindTCP, Target: target, Expected: "accepting connections"}
	// bash, not sh: /dev/tcp is a bash-ism. A sandbox image without bash
	// surfaces as an exec error below — the auditor can't check, honestly.
	res, err := env.Sandbox.Exec(ctx, []string{"bash", "-c", fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d", c.Port)})
	if err != nil {
		ev.Status, ev.Actual = Error, "exec: "+err.Error()
		return ev, nil
	}
	if res.ExitCode == 0 {
		ev.Status, ev.Actual = Pass, "connected"
		return ev, nil
	}
	detail := firstLine(res.Stderr)
	if detail == "" {
		detail = fmt.Sprintf("exit %d", res.ExitCode)
	}
	ev.Status, ev.Actual = Fail, detail
	return ev, nil
}
