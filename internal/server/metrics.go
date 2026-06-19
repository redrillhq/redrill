// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redrillhq/redrill/internal/scheduler"
	"github.com/redrillhq/redrill/internal/store"
)

// handleMetrics renders the Prometheus text exposition format. Every value is
// computed from the store at scrape time, so counters survive daemon restarts.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	families := s.gatherMetrics(r.Context())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeExposition(w, families)
}

// labeledValue is one metric sample: its label set and a preformatted value.
type labeledValue struct {
	labels [][2]string
	value  string
}

type metricFamily struct {
	name, help, typ string
	samples         []labeledValue
}

func (s *Server) gatherMetrics(ctx context.Context) []metricFamily {
	now := s.now()
	var lastProven, slaOK, runResult, runDuration, bytesRestored []labeledValue

	for i := range s.cfg.Drills {
		d := &s.cfg.Drills[i]
		drillLabel := [][2]string{{"drill", d.Name}}

		if proofs, err := s.store.ListProofs(ctx, d.Name); err != nil {
			s.log.Warn("metrics: list proofs", "drill", d.Name, "error", err.Error())
		} else {
			for _, p := range proofs {
				lastProven = append(lastProven, labeledValue{
					labels: [][2]string{{"drill", d.Name}, {"level", p.Level}},
					value:  strconv.FormatInt(p.LastProvenAt.Unix(), 10),
				})
			}
		}

		headline := scheduler.HeadlineLevel(*d)
		var provenAt time.Time
		if headline != "" {
			if at, ok, err := s.store.GetProof(ctx, d.Name, headline); err == nil && ok {
				provenAt = at
			}
		}
		stale := scheduler.Stale(d.MaxProofAge.Duration(), provenAt, now)
		slaOK = append(slaOK, labeledValue{labels: drillLabel, value: boolVal(!stale)})

		if last, ok, err := s.store.LatestFinishedRun(ctx, d.Name); err != nil {
			s.log.Warn("metrics: latest run", "drill", d.Name, "error", err.Error())
		} else if ok {
			runDuration = append(runDuration, labeledValue{
				labels: drillLabel,
				value:  strconv.FormatFloat(float64(last.DurationMS)/1000, 'f', -1, 64),
			})
			for _, res := range []store.Result{store.ResultPass, store.ResultFail, store.ResultError} {
				runResult = append(runResult, labeledValue{
					labels: [][2]string{{"drill", d.Name}, {"result", string(res)}},
					value:  boolVal(last.Result == res),
				})
			}
		}

		if total, err := s.store.SumBytesRestored(ctx, d.Name); err != nil {
			s.log.Warn("metrics: bytes restored", "drill", d.Name, "error", err.Error())
		} else {
			bytesRestored = append(bytesRestored, labeledValue{labels: drillLabel, value: strconv.FormatInt(total, 10)})
		}
	}

	scratch := labeledValue{value: strconv.FormatInt(dirSize(s.cfg.Scratch.Dir), 10)}

	return []metricFamily{
		{"redrill_last_proven_timestamp_seconds", "Unix time of the last fully-passing run, per drill and level.", "gauge", lastProven},
		{"redrill_proof_sla_ok", "1 if the drill's headline proof is within its max_proof_age, else 0.", "gauge", slaOK},
		{"redrill_run_result", "Latest finished run's result per drill (1 for the current result, else 0).", "gauge", runResult},
		{"redrill_run_duration_seconds", "Latest finished run's wall-clock duration in seconds, per drill.", "gauge", runDuration},
		{"redrill_bytes_restored_total", "Total bytes restored across a drill's runs.", "counter", bytesRestored},
		{"redrill_scratch_used_bytes", "Bytes currently used under the scratch directory.", "gauge", []labeledValue{scratch}},
	}
}

func writeExposition(w io.Writer, families []metricFamily) {
	for _, f := range families {
		fmt.Fprintf(w, "# HELP %s %s\n", f.name, f.help)
		fmt.Fprintf(w, "# TYPE %s %s\n", f.name, f.typ)
		samples := append([]labeledValue(nil), f.samples...)
		sort.Slice(samples, func(i, j int) bool { return labelKey(samples[i].labels) < labelKey(samples[j].labels) })
		for _, s := range samples {
			fmt.Fprintf(w, "%s%s %s\n", f.name, formatLabels(s.labels), s.value)
		}
	}
}

func formatLabels(labels [][2]string) string {
	if len(labels) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, kv := range labels {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[0])
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(kv[1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabelValue(v string) string { return labelEscaper.Replace(v) }

// labelKey gives a stable sort key for a sample's label set.
func labelKey(labels [][2]string) string {
	var b strings.Builder
	for _, kv := range labels {
		b.WriteString(kv[0])
		b.WriteByte('=')
		b.WriteString(kv[1])
		b.WriteByte(';')
	}
	return b.String()
}

func boolVal(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// dirSize sums the regular-file bytes under dir; unreadable entries are skipped
// so a scrape never fails on a transient FS error. 0 when dir is unset/absent.
func dirSize(dir string) int64 {
	if dir == "" {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip unreadable entries; a partial size beats a failed scrape
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
