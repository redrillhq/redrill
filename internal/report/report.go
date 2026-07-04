// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package report

import (
	"context"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/scheduler"
	"github.com/redrillhq/redrill/internal/store"
)

// Report is the assembled proof picture for every configured drill.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	ProvenOK    int       `json:"proven_within_sla"`
	Total       int       `json:"total"`
	Drills      []Drill   `json:"drills"`
}

// Drill carries only the source's name and type, never repo strings or paths —
// a repo URL can embed credentials, and the report is meant to be shared.
type Drill struct {
	Name            string  `json:"name"`
	SourceName      string  `json:"source"`
	SourceType      string  `json:"source_type"`
	Schedule        string  `json:"schedule,omitempty"` // empty = manual-only
	HeadlineLevel   string  `json:"headline_level,omitempty"`
	MaxProofAgeSecs int64   `json:"max_proof_age_seconds,omitempty"`
	Stale           bool    `json:"stale"`
	Proofs          []Proof `json:"proofs,omitempty"`
	LastRun         *Run    `json:"last_run,omitempty"`
}

type Proof struct {
	Level string    `json:"level"`
	At    time.Time `json:"at"`
}

// Run is the drill's newest run with its steps and evidence. A still-running
// run has an empty Result and a zero FinishedAt.
type Run struct {
	ID            int64      `json:"id"`
	Result        string     `json:"result"`
	Trigger       string     `json:"trigger"`
	LevelReached  string     `json:"level_reached,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    time.Time  `json:"finished_at,omitzero"`
	DurationMS    int64      `json:"duration_ms"`
	BytesRestored int64      `json:"bytes_restored"`
	FilesRestored int64      `json:"files_restored"`
	Steps         []Step     `json:"steps,omitempty"`
	Evidence      []Evidence `json:"evidence,omitempty"`
}

type Step struct {
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Summary    string `json:"summary,omitempty"`
}

type Evidence struct {
	CheckKind string `json:"check_kind"`
	Target    string `json:"target,omitempty"`
	Expected  string `json:"expected,omitempty"`
	Actual    string `json:"actual,omitempty"`
	Status    string `json:"status"`
	Weak      bool   `json:"weak,omitempty"`
}

// Build assembles the report from config (the drill set) and the store (runs,
// evidence, proofs), evaluating SLA state at now.
func Build(ctx context.Context, st *store.Store, cfg *config.Config, now time.Time) (Report, error) {
	r := Report{GeneratedAt: now, Total: len(cfg.Drills), Drills: make([]Drill, 0, len(cfg.Drills))}
	for i := range cfg.Drills {
		d := &cfg.Drills[i]
		dr := Drill{
			Name:            d.Name,
			SourceName:      d.Source,
			SourceType:      sourceType(cfg, d.Source),
			Schedule:        d.Schedule,
			HeadlineLevel:   scheduler.HeadlineLevel(*d),
			MaxProofAgeSecs: int64(d.MaxProofAge.Duration().Seconds()),
		}

		proofs, err := st.ListProofs(ctx, d.Name)
		if err != nil {
			return Report{}, err
		}
		var headline time.Time
		for _, p := range proofs {
			dr.Proofs = append(dr.Proofs, Proof{Level: p.Level, At: p.LastProvenAt})
			if p.Level == dr.HeadlineLevel {
				headline = p.LastProvenAt
			}
		}
		dr.Stale = scheduler.Stale(d.MaxProofAge.Duration(), headline, now)
		if !dr.Stale {
			r.ProvenOK++
		}

		runs, err := st.ListRuns(ctx, d.Name, 1)
		if err != nil {
			return Report{}, err
		}
		if len(runs) > 0 {
			run, err := buildRun(ctx, st, runs[0])
			if err != nil {
				return Report{}, err
			}
			dr.LastRun = &run
		}
		r.Drills = append(r.Drills, dr)
	}
	return r, nil
}

func buildRun(ctx context.Context, st *store.Store, run store.Run) (Run, error) {
	out := Run{
		ID:            run.ID,
		Result:        string(run.Result),
		Trigger:       string(run.Trigger),
		LevelReached:  run.LevelReached,
		StartedAt:     run.StartedAt,
		FinishedAt:    run.FinishedAt,
		DurationMS:    run.DurationMS,
		BytesRestored: run.BytesRestored,
		FilesRestored: run.FilesRestored,
	}
	steps, err := st.ListSteps(ctx, run.ID)
	if err != nil {
		return Run{}, err
	}
	for _, s := range steps {
		step := Step{Kind: s.Kind, Status: s.Status, Summary: s.Summary}
		if !s.FinishedAt.IsZero() && !s.StartedAt.IsZero() {
			step.DurationMS = s.FinishedAt.Sub(s.StartedAt).Milliseconds()
		}
		out.Steps = append(out.Steps, step)
	}
	evs, err := st.ListEvidence(ctx, run.ID)
	if err != nil {
		return Run{}, err
	}
	for _, e := range evs {
		out.Evidence = append(out.Evidence, Evidence{
			CheckKind: e.CheckKind, Target: e.Target,
			Expected: e.Expected, Actual: e.Actual,
			Status: e.Status, Weak: e.Weak,
		})
	}
	return out, nil
}

func sourceType(cfg *config.Config, name string) string {
	for i := range cfg.Sources {
		if cfg.Sources[i].Name == name {
			return cfg.Sources[i].Type
		}
	}
	return ""
}
