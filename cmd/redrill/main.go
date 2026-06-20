// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command redrill proves backups are restorable by running scheduled
// restore drills against them. This package is CLI wiring only; all logic
// lives under internal/.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/exec"
	"github.com/redrillhq/redrill/internal/orchestrate"
	"github.com/redrillhq/redrill/internal/sandbox/docker"
	"github.com/redrillhq/redrill/internal/scheduler"
	"github.com/redrillhq/redrill/internal/store"
)

const defaultConfigPath = "/etc/redrill/config.yaml"

// configFileDefault is the -c flag's default, honoring $REDRILL_CONFIG.
func configFileDefault() string {
	if p := os.Getenv("REDRILL_CONFIG"); p != "" {
		return p
	}
	return defaultConfigPath
}

// printConfigError reports a failed config load. A missing file gets a short,
// actionable message pointing at the override; anything else keeps the detail.
func printConfigError(stderr io.Writer, path string, err error) {
	if errors.Is(err, os.ErrNotExist) {
		if path == defaultConfigPath {
			fmt.Fprintf(stderr, "redrill: no config file at %s (the default path)\n", path)
			fmt.Fprintln(stderr, "hint: set a config path with -c <file> or $REDRILL_CONFIG")
		} else {
			fmt.Fprintf(stderr, "redrill: no config file at %s\n", path)
		}
		return
	}
	fmt.Fprintf(stderr, "redrill: %s is invalid:\n%v\n", path, err)
}

// printDrillNames lists the drills defined in cfg, to help pick a NAME.
func printDrillNames(w io.Writer, cfg *config.Config, path string) {
	if len(cfg.Drills) == 0 {
		fmt.Fprintf(w, "no drills configured in %s\n", path)
		return
	}
	names := make([]string, len(cfg.Drills))
	for i := range cfg.Drills {
		names[i] = cfg.Drills[i].Name
	}
	fmt.Fprintf(w, "configured drills: %s\n", strings.Join(names, ", "))
}

// Set at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const usage = `redrill — scheduled restore drills for backups

Usage:
  redrill <command> [flags]

Commands:
  validate   strictly check a config file
  run        run a drill now: NAME, --all, or pick interactively
  status     show each drill's last run, proof age, next run, and SLA state
  history    show a drill's past runs
  serve      run the scheduler daemon
  doctor     check the environment: engines, container runtime, scratch, repos
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
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "history":
		return runHistory(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
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
	path := fs.String("c", configFileDefault(), "config file path (or set $REDRILL_CONFIG)")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	cfg, err := config.Load(*path)
	if err == nil {
		// config is a leaf and can't reach the scheduler grammar or shoutrrr, so
		// check schedules and notify URLs here.
		err = extraValidate(cfg)
	}
	if err != nil {
		if *jsonOut {
			writeJSON(stdout, map[string]any{
				"valid":  false,
				"config": *path,
				"errors": errorLines(err),
			})
		} else {
			printConfigError(stderr, *path, err)
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

// Splits a joined error into one message per line for JSON output.
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
	enc.SetEscapeHTML(false) // keep expect predicates ("> 0", "age < 8d") readable
	_ = enc.Encode(v)
}

func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", configFileDefault(), "config file path (or set $REDRILL_CONFIG)")
	level := fs.String("level", "", "run only this level (l1|l2|l3)")
	all := fs.Bool("all", false, "run every configured drill, sequentially")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	name, ok, err := parseNameAndFlags(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		printConfigError(stderr, *path, err)
		return 3
	}

	// Resolve which drills to run: --all, the named one, or an interactive pick.
	names, interactive, code := selectDrills(stderr, cfg, *path, name, ok, *all)
	switch {
	case interactive:
		// A picker only makes sense on a terminal; cron/scripts/--json require a NAME.
		if *jsonOut || !isTTY(os.Stdin) {
			fmt.Fprintln(stderr, "redrill: run requires a drill NAME")
			printDrillNames(stderr, cfg, *path)
			return 2
		}
		picked, c := pickDrill(os.Stdin, stdout, cfg)
		if picked == "" {
			return c
		}
		names = []string{picked}
	case len(names) == 0:
		return code
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
	executor := exec.NewLocal(host).WithIOPolicy(ioPolicy(cfg))
	// Wire the L3 sandbox if a container engine is reachable, else L3 skips.
	// The janitor reaps orphans from crashed runs.
	if rt, rerr := docker.NewRuntime(ctx); rerr == nil {
		defer func() { _ = rt.Close() }()
		_, _ = rt.Janitor(ctx)
		executor.WithSandbox(rt)
	}
	o := orchestrate.New(st, executor, func() time.Time { return time.Now().UTC() })

	// Run each selected drill, tracking the worst result for the exit code.
	worst := store.ResultPass
	results := make([]any, 0, len(names))
	for _, n := range names {
		drill, src, _ := findDrill(cfg, n) // n came from cfg, so it always resolves
		if len(names) > 1 && !*jsonOut {
			fmt.Fprintf(stdout, "== %s ==\n", n)
		}
		opts := orchestrate.RunOptions{Trigger: store.TriggerManual, Level: *level, Scratch: cfg.Scratch}
		if !*jsonOut {
			opts.Report = func(out orchestrate.LevelOutcome) { printLevel(stdout, out) }
		}
		res, rerr := o.Run(ctx, *drill, *src, opts)
		if rerr != nil {
			fmt.Fprintf(stderr, "redrill: %s: %v\n", n, rerr)
			worst = worseResult(worst, store.ResultError)
			continue
		}
		if *jsonOut {
			results = append(results, runResultJSON(n, res))
		} else {
			fmt.Fprintf(stdout, "redrill: %s → %s (reached %s, run %d)\n",
				n, strings.ToUpper(string(res.Status)), res.LevelReached, res.RunID)
		}
		worst = worseResult(worst, res.Status)
	}
	if *jsonOut {
		if *all {
			writeJSON(stdout, results) // an array, one entry per drill
		} else if len(results) > 0 {
			writeJSON(stdout, results[0]) // a single drill stays a bare object
		}
	}
	return resultExit(worst)
}

// selectDrills decides which drills `run` should execute from its flags: every
// drill (--all), the named one, or — with neither — interactive=true so the
// caller can prompt. A zero-length, non-interactive result means stop with code.
func selectDrills(stderr io.Writer, cfg *config.Config, path, name string, haveName, all bool) (names []string, interactive bool, code int) {
	switch {
	case all:
		if haveName {
			fmt.Fprintln(stderr, "redrill: run --all takes no drill NAME")
			return nil, false, 2
		}
		if len(cfg.Drills) == 0 {
			printDrillNames(stderr, cfg, path)
			return nil, false, 2
		}
		out := make([]string, len(cfg.Drills))
		for i := range cfg.Drills {
			out[i] = cfg.Drills[i].Name
		}
		return out, false, 0
	case haveName:
		if _, _, ok := findDrill(cfg, name); !ok {
			fmt.Fprintf(stderr, "redrill: no drill named %q in %s\n", name, path)
			printDrillNames(stderr, cfg, path)
			return nil, false, 2
		}
		return []string{name}, false, 0
	default:
		if len(cfg.Drills) == 0 {
			fmt.Fprintln(stderr, "redrill: run requires a drill NAME")
			printDrillNames(stderr, cfg, path)
			return nil, false, 2
		}
		return nil, true, 0
	}
}

// pickDrill prompts for one drill from cfg, reading the choice from in. It
// returns the chosen name, or "" with an exit code when nothing is selected.
func pickDrill(in io.Reader, out io.Writer, cfg *config.Config) (string, int) {
	fmt.Fprintln(out, "select a drill to run:")
	for i := range cfg.Drills {
		fmt.Fprintf(out, "  %d) %s\n", i+1, cfg.Drills[i].Name)
	}
	fmt.Fprint(out, "drill number (blank to cancel): ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return "", 0
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(cfg.Drills) {
		fmt.Fprintf(out, "no drill matches %q\n", line)
		return "", 2
	}
	return cfg.Drills[n-1].Name, 0
}

// worseResult returns the more severe of two results — fail outranks error
// outranks pass — matching the per-run aggregation in orchestrate.
func worseResult(a, b store.Result) store.Result {
	switch {
	case a == store.ResultFail || b == store.ResultFail:
		return store.ResultFail
	case a == store.ResultError || b == store.ResultError:
		return store.ResultError
	default:
		return store.ResultPass
	}
}

func findDrill(cfg *config.Config, name string) (*config.Drill, *config.Source, bool) {
	for i := range cfg.Drills {
		if cfg.Drills[i].Name != name {
			continue
		}
		d := &cfg.Drills[i]
		if src, ok := findSource(cfg, d.Source); ok {
			return d, src, true
		}
		return nil, nil, false
	}
	return nil, nil, false
}

func findSource(cfg *config.Config, name string) (*config.Source, bool) {
	for i := range cfg.Sources {
		if cfg.Sources[i].Name == name {
			return &cfg.Sources[i], true
		}
	}
	return nil, false
}

// parseNameAndFlags parses a subcommand whose flags may sit on either side of a
// required positional NAME. ok is false when no NAME was given.
func parseNameAndFlags(fs *flag.FlagSet, args []string) (name string, ok bool, err error) {
	if err := fs.Parse(args); err != nil {
		return "", false, err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", false, nil
	}
	// flag.Parse stops at the first positional, so re-parse what follows NAME.
	if err := fs.Parse(rest[1:]); err != nil {
		return "", false, err
	}
	return rest[0], true, nil
}

func checkSchedules(cfg *config.Config) error {
	var errs []error
	for i := range cfg.Drills {
		if _, err := scheduler.ParseSchedule(cfg.Drills[i].Schedule); err != nil {
			errs = append(errs, fmt.Errorf("drills[%d].schedule: %w", i, err))
		}
	}
	return errors.Join(errs...)
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
