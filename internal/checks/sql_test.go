// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"context"
	"errors"
	"testing"

	"github.com/alyamovsky/redrill/internal/sandbox"
)

type fakeSandbox struct {
	out  string
	exit int
	err  error
}

func (f fakeSandbox) Endpoint(string) (string, error) { return "", nil }
func (f fakeSandbox) Exec(context.Context, []string) (sandbox.ExecResult, error) {
	if f.err != nil {
		return sandbox.ExecResult{}, f.err
	}
	return sandbox.ExecResult{Stdout: f.out, Stderr: "ERROR: relation does not exist", ExitCode: f.exit}, nil
}
func (f fakeSandbox) Close(context.Context) error { return nil }

func TestSQLCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name   string
		out    string
		exit   int
		expect string
		want   Status
	}{
		{"value passes", "42", 0, "> 0", Pass},
		{"value fails", "0", 0, "> 0", Fail},
		{"query errors", "", 1, "> 0", Error},
		{"coercion fails", "notanumber", 0, "> 0", Error},
		{"bad expect predicate", "42", 0, "> abc", Error},
	}
	for _, tc := range cases {
		ev, _ := SQL{Query: "select count(*) from users", Expect: tc.expect, DB: "app"}.
			Run(ctx, CheckEnv{Sandbox: fakeSandbox{out: tc.out, exit: tc.exit}})
		if ev.Status != tc.want {
			t.Errorf("%s: status %s, want %s (actual %q)", tc.name, ev.Status, tc.want, ev.Actual)
		}
	}

	ev, _ := SQL{Query: "q", Expect: "> 0"}.Run(ctx, CheckEnv{Sandbox: fakeSandbox{err: errors.New("docker down")}})
	if ev.Status != Error {
		t.Errorf("exec transport error: %s, want error", ev.Status)
	}
}

func TestSQLNoError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if ev, _ := (SQLNoError{Query: "select 1"}).Run(ctx, CheckEnv{Sandbox: fakeSandbox{exit: 0}}); ev.Status != Pass {
		t.Errorf("clean query: %s, want pass", ev.Status)
	}
	if ev, _ := (SQLNoError{Query: "select * from nope"}).Run(ctx, CheckEnv{Sandbox: fakeSandbox{exit: 1}}); ev.Status != Fail {
		t.Errorf("erroring query: %s, want fail", ev.Status)
	}
}
