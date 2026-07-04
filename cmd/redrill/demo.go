// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/exec"
	"github.com/redrillhq/redrill/internal/orchestrate"
	"github.com/redrillhq/redrill/internal/sandbox/docker"
	"github.com/redrillhq/redrill/internal/store"
)

const demoUsage = `usage: redrill demo sabotage [--no-sandbox]

The sabotage demo builds a throwaway backup, proves it restorable, then breaks
it the way real backups die — file present, timestamp fresh, contents dead —
and shows the drill catching it. Nothing outside the demo workspace is touched.
`

// demoUsers is the row count the healthy dump carries and the L3 SQL assert
// expects.
const demoUsers = 200

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noSandbox := fs.Bool("no-sandbox", false, "skip the database-boot level even when a container runtime is present")
	name, ok, err := parseNameAndFlags(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !ok || name != "sabotage" {
		fmt.Fprint(stderr, demoUsage)
		return 2
	}
	return demoSabotage(*noSandbox, stdout, stderr)
}

// demoSabotage runs the whole show: healthy PASS, sabotage, caught FAIL.
// Exit 0 only when both verdicts came out as designed; anything else is 2.
func demoSabotage(noSandbox bool, stdout, stderr io.Writer) int {
	ctx := context.Background()
	ws, err := os.MkdirTemp("", "redrill-demo-*")
	if err != nil {
		fmt.Fprintf(stderr, "redrill: cannot create demo workspace: %v\n", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(ws) }()
	for _, d := range []string{"backups", "data", "scratch"} {
		if err := os.MkdirAll(filepath.Join(ws, d), 0o700); err != nil {
			fmt.Fprintf(stderr, "redrill: %v\n", err)
			return 2
		}
	}

	dumpName := "app-db-" + time.Now().UTC().Format("2006-01-02") + ".sql.gz"
	dumpPath := filepath.Join(ws, "backups", dumpName)
	if err := writeDemoDump(dumpPath); err != nil {
		fmt.Fprintf(stderr, "redrill: %v\n", err)
		return 2
	}

	// The database-boot level joins in only when a container runtime answers,
	// so the demo works — and stays honest — on a bare host too.
	var rt *docker.Runtime
	if !noSandbox {
		if r, rerr := docker.NewRuntime(ctx); rerr == nil {
			rt = r
			defer func() { _ = rt.Close() }()
		}
	}

	yaml := demoConfigYAML(ws, rt != nil)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		// The rendered config is validated before use; failing here is a redrill
		// bug, never a user problem.
		fmt.Fprintf(stderr, "redrill: internal error: demo config did not validate: %v\n", err)
		return 2
	}

	fmt.Fprint(stdout, "== redrill demo: sabotage ==\n\n")
	fmt.Fprint(stdout, "The scenario: a cron job dumps a PostgreSQL database every night. The file\narrives on schedule and monitoring stays green — whether the backup is alive\nor not. This demo kills the backup without breaking any of those signals.\n\n")
	fmt.Fprintf(stdout, "Throwaway workspace (discarded at the end): %s\n", ws)
	fmt.Fprintf(stdout, "  backup: backups/%s — a PostgreSQL dump with %d users\n", dumpName, demoUsers)
	switch {
	case rt != nil:
		fmt.Fprint(stdout, "  sandbox: container runtime found — the drill will boot a disposable\n  PostgreSQL from the restored dump (first run may pull postgres:16)\n")
	case noSandbox:
		fmt.Fprint(stdout, "  sandbox: skipped (--no-sandbox) — file-integrity checks only\n")
	default:
		fmt.Fprint(stdout, "  sandbox: no container runtime reachable — file-integrity checks only\n")
	}
	fmt.Fprintf(stdout, "  config:\n\n    %s\n", strings.ReplaceAll(strings.TrimRight(yaml, "\n"), "\n", "\n    "))

	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "redrill.db"))
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %v\n", err)
		return 2
	}
	defer func() { _ = st.Close() }()

	host, _ := os.Hostname()
	executor := exec.NewLocal(host)
	if rt != nil {
		executor.WithSandbox(rt)
	}
	o := orchestrate.New(st, executor, func() time.Time { return time.Now().UTC() })
	drill, src, _ := findDrill(cfg, "app-db")
	runDrill := func() (store.Result, error) {
		res, err := o.Run(ctx, *drill, *src, orchestrate.RunOptions{
			Trigger: store.TriggerManual, Scratch: cfg.Scratch,
			Report: func(out orchestrate.LevelOutcome) { printLevel(stdout, out) },
		})
		if err != nil {
			return store.ResultError, err
		}
		return res.Status, nil
	}

	fmt.Fprint(stdout, "\n[1/3] drill against the healthy backup\n")
	if status, err := runDrill(); err != nil || status != store.ResultPass {
		if err != nil {
			fmt.Fprintf(stderr, "redrill: demo drill errored: %v\n", err)
		}
		fmt.Fprint(stderr, "redrill: the healthy drill did not pass — the demo environment is the\nproblem (engine or sandbox, not the verdict logic). With Docker present but\nunhealthy, --no-sandbox skips the database level.\n")
		return 2
	}
	fmt.Fprint(stdout, "result: PASS — restorability proven, proof recorded\n")

	fmt.Fprint(stdout, "\n[2/3] sabotage: the dump is emptied to 0 bytes, its timestamp kept fresh\n")
	now := time.Now()
	if err := os.Truncate(dumpPath, 0); err != nil {
		fmt.Fprintf(stderr, "redrill: sabotage step failed: %v\n", err)
		return 2
	}
	if err := os.Chtimes(dumpPath, now, now); err != nil {
		fmt.Fprintf(stderr, "redrill: sabotage step failed: %v\n", err)
		return 2
	}
	fmt.Fprint(stdout, "      cron exit code: still 0 · file: present · mtime: seconds old.\n      Every \"did the backup run?\" monitor stays green.\n")

	fmt.Fprint(stdout, "\n[3/3] drill against the sabotaged backup\n")
	status, err := runDrill()
	if err != nil {
		fmt.Fprintf(stderr, "redrill: the sabotaged drill errored instead of failing: %v\n", err)
		return 2
	}
	if status != store.ResultFail {
		fmt.Fprintf(stderr, "redrill: the sabotage was NOT caught (got %s, want fail) — this is a redrill bug; please report it\n", status)
		return 2
	}
	fmt.Fprint(stdout, "result: FAIL — the dead backup was caught; the proof does not advance\n")

	fmt.Fprint(stdout, "\nfail means the backup is bad. error would mean redrill could not check —\nnever a silent pass. The workspace is discarded; a real setup starts with:\n  redrill init\n")
	return 0
}

// writeDemoDump renders a plausible plain-format pg_dump with demoUsers rows
// and gzips it to path.
func writeDemoDump(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: path is inside the demo's own temp workspace
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if _, err = zw.Write(demoSQL()); err == nil {
		err = zw.Close()
	} else {
		_ = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func demoSQL() []byte {
	var b strings.Builder
	b.WriteString(`--
-- PostgreSQL database dump
--

SET statement_timeout = 0;
SET client_encoding = 'UTF8';

CREATE TABLE users (
    id integer PRIMARY KEY,
    email text NOT NULL,
    password_hash text NOT NULL
);

`)
	for i := 1; i <= demoUsers; i++ {
		// Deterministic mock hashes; their entropy keeps the gzip comfortably
		// above the drill's file_min_bytes.
		h := sha256.Sum256([]byte{byte(i), byte(i >> 8)})
		fmt.Fprintf(&b, "INSERT INTO users VALUES (%d, 'user%d@example.org', '%x');\n", i, i, h[:16])
	}
	b.WriteString("\n--\n-- PostgreSQL database dump complete\n--\n")
	return []byte(b.String())
}

// demoConfigYAML renders the demo's config; the caller re-validates it via
// config.Parse before use (the init trust guarantee).
func demoConfigYAML(ws string, sandbox bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "version: 1\ndata_dir: %q\nscratch: {dir: %q}\n", filepath.Join(ws, "data"), filepath.Join(ws, "scratch"))
	fmt.Fprintf(&b, "sources:\n  - name: nightly-dumps\n    type: dumpdir\n    path: %q\n    pattern: \"*.sql.gz\"\n", filepath.Join(ws, "backups"))
	b.WriteString(`drills:
  - name: app-db
    source: nightly-dumps
    max_proof_age: 8d
    levels:
      l1:
        file_min_bytes: 1KiB
        compression_test: true
        max_age: 2d
`)
	if sandbox {
		fmt.Fprintf(&b, `      l3:
        sandbox: {image: postgres:16}
        checks:
          - sql: {query: "select count(*) from users", expect: "== %d"}
`, demoUsers)
	}
	return b.String()
}
