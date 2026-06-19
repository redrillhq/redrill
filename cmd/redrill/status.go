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

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/scheduler"
	"github.com/redrillhq/redrill/internal/store"
)

type drillStatus struct {
	drill         string
	source        string
	last          *store.Run         // newest run, nil if none
	proofs        []store.DrillState // last_proven per level
	headlineLevel string             // highest configured level
	headlineProof time.Time
	headlineOK    bool
	nextRun       time.Time // zero if schedule unparseable
	nextOK        bool
	stale         bool
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", defaultConfigPath, "config file path")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
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

	now := time.Now().UTC()
	statuses, err := collectStatus(ctx, st, cfg, now)
	if err != nil {
		fmt.Fprintf(stderr, "redrill: %v\n", err)
		return 2
	}

	if *jsonOut {
		writeJSON(stdout, statusJSON(statuses))
	} else {
		printStatus(stdout, statuses, now)
	}
	return 0
}

func collectStatus(ctx context.Context, st *store.Store, cfg *config.Config, now time.Time) ([]drillStatus, error) {
	out := make([]drillStatus, 0, len(cfg.Drills))
	for i := range cfg.Drills {
		d := &cfg.Drills[i]
		ds := drillStatus{drill: d.Name, source: d.Source, headlineLevel: scheduler.HeadlineLevel(*d)}

		runs, err := st.ListRuns(ctx, d.Name, 1)
		if err != nil {
			return nil, err
		}
		if len(runs) > 0 {
			ds.last = &runs[0]
		}
		if ds.proofs, err = st.ListProofs(ctx, d.Name); err != nil {
			return nil, err
		}
		if ds.headlineLevel != "" {
			if at, ok, err := st.GetProof(ctx, d.Name, ds.headlineLevel); err != nil {
				return nil, err
			} else if ok {
				ds.headlineProof, ds.headlineOK = at, true
			}
		}
		if sched, err := scheduler.ParseSchedule(d.Schedule); err == nil {
			ds.nextRun, ds.nextOK = sched.Next(now), true
		}
		ds.stale = scheduler.Stale(d.MaxProofAge.Duration(), ds.headlineProof, now)
		out = append(out, ds)
	}
	return out, nil
}

func printStatus(w io.Writer, list []drillStatus, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DRILL\tLAST RUN\tPROVEN\tNEXT RUN\tSLA")
	okCount := 0
	for _, d := range list {
		sla := "ok"
		if d.stale {
			sla = "STALE"
		} else {
			okCount++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			d.drill, lastRunCell(d, now), provenCell(d, now), nextRunCell(d, now), sla)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\n%d of %d drills proven within SLA\n", okCount, len(list))
}

func lastRunCell(d drillStatus, now time.Time) string {
	switch {
	case d.last == nil:
		return "never"
	case d.last.Result == "":
		return "running"
	default:
		return fmt.Sprintf("%s %s", d.last.Result, ago(now, d.last.FinishedAt))
	}
}

func provenCell(d drillStatus, now time.Time) string {
	if d.headlineLevel == "" {
		return "-"
	}
	if !d.headlineOK {
		return d.headlineLevel + " never"
	}
	return fmt.Sprintf("%s %s", d.headlineLevel, ago(now, d.headlineProof))
}

func nextRunCell(d drillStatus, now time.Time) string {
	if !d.nextOK {
		return "-"
	}
	return until(now, d.nextRun)
}

// Optional times are RFC3339, omitted when absent.
func statusJSON(list []drillStatus) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		m := map[string]any{
			"drill":  d.drill,
			"source": d.source,
			"stale":  d.stale,
		}
		if d.headlineLevel != "" {
			m["headline_level"] = d.headlineLevel
		}
		if d.last != nil {
			m["last_result"] = string(d.last.Result)
			m["level_reached"] = d.last.LevelReached
			if !d.last.FinishedAt.IsZero() {
				m["last_run_at"] = d.last.FinishedAt.Format(time.RFC3339)
			}
		}
		if d.headlineOK {
			m["last_proven"] = d.headlineProof.Format(time.RFC3339)
		}
		if d.nextOK {
			m["next_run"] = d.nextRun.Format(time.RFC3339)
		}
		if len(d.proofs) > 0 {
			proofs := make(map[string]string, len(d.proofs))
			for _, p := range d.proofs {
				proofs[p.Level] = p.LastProvenAt.Format(time.RFC3339)
			}
			m["proofs"] = proofs
		}
		out = append(out, m)
	}
	return out
}

// "3d ago"; a zero t reads as "never".
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return humanDur(now.Sub(t)) + " ago"
}

// "in 3d"; a zero or past t reads as "now".
func until(now, t time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "now"
	}
	return "in " + humanDur(d)
}

// One unit of resolution.
func humanDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
