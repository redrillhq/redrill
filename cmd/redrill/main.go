// Command redrill proves backups are restorable by running scheduled
// restore drills against them. This package is CLI wiring only; all logic
// lives under internal/.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/exec"
	"github.com/alyamovsky/redrill/internal/orchestrate"
	"github.com/alyamovsky/redrill/internal/sandbox/docker"
	"github.com/alyamovsky/redrill/internal/store"
)

const defaultConfigPath = "/etc/redrill/config.yaml"

// Set at build time via -ldflags (see Makefile).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const usage = `redrill — scheduled restore drills for your backups

Usage:
  redrill <command> [flags]

Commands:
  validate   strictly check a config file
  run        run one drill now and print the result
  version    print version information

Exit codes: 0 ok · 1 drill fail · 2 infra error · 3 config error
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "run":
		return runRun(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "redrill: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *jsonOut {
		info := map[string]string{
			"version": version,
			"commit":  commit,
			"date":    date,
			"go":      runtime.Version(),
		}
		if err := json.NewEncoder(stdout).Encode(info); err != nil {
			fmt.Fprintf(stderr, "redrill: %v\n", err)
			return 2
		}
		return 0
	}
	fmt.Fprintf(stdout, "redrill %s (commit %s, built %s, %s)\n", version, commit, date, runtime.Version())
	return 0
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", defaultConfigPath, "config file path")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	_, err := config.Load(*path)
	if err != nil {
		if *jsonOut {
			writeJSON(stdout, map[string]any{
				"valid":  false,
				"config": *path,
				"errors": errorLines(err),
			})
		} else {
			fmt.Fprintf(stderr, "redrill: %s is invalid:\n%v\n", *path, err)
		}
		return 3
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"valid": true, "config": *path})
	} else {
		fmt.Fprintf(stdout, "redrill: %s is valid\n", *path)
	}
	return 0
}

// errorLines splits a (possibly joined) error into one message per line for
// JSON output.
func errorLines(err error) []string {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // don't escape <, >, & — keep expect predicates ("> 0", "age < 8d") readable
	_ = enc.Encode(v)
}

func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", defaultConfigPath, "config file path")
	level := fs.String("level", "", "run only this level (l1|l2|l3)")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "redrill: run requires a drill NAME")
		return 2
	}
	name := rest[0]
	// flag stops at the first positional, so re-parse what follows NAME — this
	// lets flags appear on either side (e.g. `run app-db --json`).
	if err := fs.Parse(rest[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %s is invalid:\n%v\n", *path, err)
		return 3
	}
	drill, src, ok := findDrill(cfg, name)
	if !ok {
		fmt.Fprintf(stderr, "redrill: no drill named %q in %s\n", name, *path)
		return 2
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "redrill: cannot create data_dir %s: %v\n", cfg.DataDir, err)
		return 2
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "redrill.db"))
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %v\n", err)
		return 2
	}
	defer func() { _ = st.Close() }()

	host, _ := os.Hostname()
	executor := exec.NewLocal(host)
	// Wire the L3 sandbox runtime if a container engine is reachable; without
	// one, L3 degrades to skipped. The janitor reaps orphans from crashed runs.
	if rt, rerr := docker.NewRuntime(ctx); rerr == nil {
		defer func() { _ = rt.Close() }()
		_, _ = rt.Janitor(ctx)
		executor.WithSandbox(rt)
	}
	o := orchestrate.New(st, executor, func() time.Time { return time.Now().UTC() })
	opts := orchestrate.RunOptions{Trigger: store.TriggerManual, Level: *level, Scratch: cfg.Scratch}
	if !*jsonOut {
		opts.Report = func(out orchestrate.LevelOutcome) { printLevel(stdout, out) }
	}
	res, err := o.Run(ctx, *drill, *src, opts)
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %v\n", err)
		return 2
	}
	if *jsonOut {
		writeJSON(stdout, runResultJSON(name, res))
	} else {
		fmt.Fprintf(stdout, "redrill: %s → %s (reached %s, run %d)\n",
			name, strings.ToUpper(string(res.Status)), res.LevelReached, res.RunID)
	}
	return resultExit(res.Status)
}

// findDrill resolves a drill by name and its source. Config validation
// guarantees a drill's source exists, so a missing source means an unknown drill.
func findDrill(cfg *config.Config, name string) (*config.Drill, *config.Source, bool) {
	for i := range cfg.Drills {
		if cfg.Drills[i].Name != name {
			continue
		}
		d := &cfg.Drills[i]
		for j := range cfg.Sources {
			if cfg.Sources[j].Name == d.Source {
				return d, &cfg.Sources[j], true
			}
		}
		return nil, nil, false
	}
	return nil, nil, false
}

func printLevel(w io.Writer, out orchestrate.LevelOutcome) {
	fmt.Fprintf(w, "[%s] %s — %s\n", out.Level, strings.ToUpper(out.Status), out.Summary)
	for _, ev := range out.Evidence {
		weak := ""
		if ev.Weak {
			weak = " (weak)"
		}
		fmt.Fprintf(w, "  %-5s %-22s %-16s expected %q actual %q%s\n",
			strings.ToUpper(string(ev.Status)), ev.Kind, ev.Target, ev.Expected, ev.Actual, weak)
	}
}

func resultExit(s store.Result) int {
	switch s {
	case store.ResultPass:
		return 0
	case store.ResultFail:
		return 1
	default:
		return 2
	}
}

func runResultJSON(drill string, res orchestrate.RunResult) any {
	type checkJSON struct {
		Kind     string `json:"kind"`
		Target   string `json:"target"`
		Expected string `json:"expected"`
		Actual   string `json:"actual"`
		Status   string `json:"status"`
		Weak     bool   `json:"weak,omitempty"`
	}
	type levelJSON struct {
		Level   string      `json:"level"`
		Status  string      `json:"status"`
		Summary string      `json:"summary"`
		Checks  []checkJSON `json:"checks,omitempty"`
	}
	out := struct {
		RunID        int64       `json:"run_id"`
		Drill        string      `json:"drill"`
		Status       string      `json:"status"`
		LevelReached string      `json:"level_reached"`
		Levels       []levelJSON `json:"levels"`
	}{RunID: res.RunID, Drill: drill, Status: string(res.Status), LevelReached: res.LevelReached}
	for _, l := range res.Levels {
		lj := levelJSON{Level: l.Level, Status: l.Status, Summary: l.Summary}
		for _, ev := range l.Evidence {
			lj.Checks = append(lj.Checks, checkJSON{
				Kind: ev.Kind, Target: ev.Target, Expected: ev.Expected,
				Actual: ev.Actual, Status: string(ev.Status), Weak: ev.Weak,
			})
		}
		out.Levels = append(out.Levels, lj)
	}
	return out
}
