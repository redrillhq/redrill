// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/store"
)

func runHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", defaultConfigPath, "config file path")
	limit := fs.Int("n", 20, "max number of runs to show")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	name, ok, err := parseNameAndFlags(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !ok {
		fmt.Fprintln(stderr, "redrill: history requires a drill NAME")
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %s is invalid:\n%v\n", *path, err)
		return 3
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

	runs, err := st.ListRuns(ctx, name, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %v\n", err)
		return 2
	}

	if *jsonOut {
		writeJSON(stdout, historyJSON(runs))
	} else {
		printHistory(stdout, name, runs)
	}
	return 0
}

func printHistory(w io.Writer, name string, runs []store.Run) {
	if len(runs) == 0 {
		fmt.Fprintf(w, "no runs recorded for %s\n", name)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tRESULT\tLEVEL\tTRIGGER\tSTARTED (UTC)\tDURATION")
	for _, r := range runs {
		result := string(r.Result)
		if result == "" {
			result = "running"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, result, dash(r.LevelReached), r.Trigger,
			r.StartedAt.Format("2006-01-02 15:04"), humanMS(r.DurationMS))
	}
	_ = tw.Flush()
}

func historyJSON(runs []store.Run) []map[string]any {
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		m := map[string]any{
			"id":             r.ID,
			"result":         string(r.Result),
			"trigger":        string(r.Trigger),
			"level_reached":  r.LevelReached,
			"started_at":     r.StartedAt.Format(time.RFC3339),
			"duration_ms":    r.DurationMS,
			"bytes_restored": r.BytesRestored,
			"files_restored": r.FilesRestored,
		}
		if !r.FinishedAt.IsZero() {
			m["finished_at"] = r.FinishedAt.Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// "3.2s", "850ms".
func humanMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
