// Command drillbit proves backups are restorable by running scheduled
// restore drills against them. This package is CLI wiring only; all logic
// lives under internal/.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/alyamovsky/drillbit/internal/config"
)

const defaultConfigPath = "/etc/drillbit/config.yaml"

// Set at build time via -ldflags (see Makefile).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const usage = `drillbit — scheduled restore drills for your backups

Usage:
  drillbit <command> [flags]

Commands:
  validate   strictly check a config file
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
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "drillbit: unknown command %q\n\n%s", args[0], usage)
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
			fmt.Fprintf(stderr, "drillbit: %v\n", err)
			return 2
		}
		return 0
	}
	fmt.Fprintf(stdout, "drillbit %s (commit %s, built %s, %s)\n", version, commit, date, runtime.Version())
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
			fmt.Fprintf(stderr, "drillbit: %s is invalid:\n%v\n", *path, err)
		}
		return 3
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"valid": true, "config": *path})
	} else {
		fmt.Fprintf(stdout, "drillbit: %s is valid\n", *path)
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
	_ = enc.Encode(v)
}
