// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"context"
	"strings"
)

// Run via psql inside the sandbox.
const (
	kindSQL        = "sql"
	kindSQLNoError = "sql_no_error"
)

// A query error or uncoercible value is Error; a value failing the predicate is Fail.
type SQL struct {
	Query  string
	Expect string
	DB     string
}

func (c SQL) Kind() string { return kindSQL }

func (c SQL) Run(ctx context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindSQL, Target: c.Query, Expected: c.Expect}
	exp, err := ParseExpect(c.Expect)
	if err != nil {
		ev.Status, ev.Actual = Error, "invalid expect: "+err.Error()
		return ev, nil
	}
	res, err := env.Sandbox.Exec(ctx, psqlCmd(c.DB, c.Query))
	if err != nil {
		ev.Status, ev.Actual = Error, "exec: "+err.Error()
		return ev, nil
	}
	if res.ExitCode != 0 {
		ev.Status, ev.Actual = Error, "query failed: "+firstLine(res.Stderr)
		return ev, nil
	}
	actual := strings.TrimSpace(res.Stdout)
	ev.Actual = actual
	ok, err := exp.Evaluate(actual, env.Now)
	if err != nil {
		ev.Status, ev.Actual = Error, actual+": "+err.Error()
		return ev, nil
	}
	ev.Status = Fail
	if ok {
		ev.Status = Pass
	}
	return ev, nil
}

// An erroring query means the data is bad, so Fail (not Error).
type SQLNoError struct {
	Query string
	DB    string
}

func (c SQLNoError) Kind() string { return kindSQLNoError }

func (c SQLNoError) Run(ctx context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindSQLNoError, Target: c.Query, Expected: "no error"}
	res, err := env.Sandbox.Exec(ctx, psqlCmd(c.DB, c.Query))
	if err != nil {
		ev.Status, ev.Actual = Error, "exec: "+err.Error()
		return ev, nil
	}
	if res.ExitCode == 0 {
		ev.Status, ev.Actual = Pass, "ok"
	} else {
		ev.Status, ev.Actual = Fail, firstLine(res.Stderr)
	}
	return ev, nil
}

// Tuples-only, unaligned, ON_ERROR_STOP so a failed query exits non-zero.
func psqlCmd(db, query string) []string {
	if db == "" {
		db = "postgres"
	}
	return []string{"psql", "-U", "postgres", "-d", db, "-tAqX", "-v", "ON_ERROR_STOP=1", "-c", query}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
