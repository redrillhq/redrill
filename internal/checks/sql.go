package checks

import (
	"context"
	"strings"
)

// L3 SQL check kinds (DESIGN §6, §7). They run psql inside the sandbox via
// CheckEnv.Sandbox — there is no network to the container (network=none), so all
// queries go through Exec.

const (
	kindSQL        = "sql"
	kindSQLNoError = "sql_no_error"
)

// SQL runs a scalar query and evaluates the result against an expect predicate.
// A query that errors is Error (the auditor couldn't get a value); a value that
// fails the predicate is Fail; a value that won't coerce is Error.
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

// SQLNoError passes iff the query runs without error — an erroring query means
// the restored data is bad (a missing table, a broken view), so it is Fail.
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

// psqlCmd runs one query, tuples-only and unaligned (a bare scalar), stopping on
// the first error so a failed query exits non-zero.
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
