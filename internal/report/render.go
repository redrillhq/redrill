// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package report

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

const dateLayout = "2006-01-02 15:04 UTC"

// Markdown renders the report as a shareable Markdown document.
func Markdown(r Report) []byte {
	var b strings.Builder
	b.WriteString("# Redrill proof report\n\n")
	fmt.Fprintf(&b, "Generated %s.\n\n", r.GeneratedAt.UTC().Format(dateLayout))
	fmt.Fprintf(&b, "**%d of %d datasets proven within SLA.**\n", r.ProvenOK, r.Total)

	for i := range r.Drills {
		d := &r.Drills[i]
		fmt.Fprintf(&b, "\n## %s — %s\n\n", mdEscape(d.Name), slaLine(d))
		fmt.Fprintf(&b, "- Source: %s (%s)\n", mdEscape(d.SourceName), d.SourceType)
		fmt.Fprintf(&b, "- Schedule: %s\n", scheduleLine(d))
		b.WriteString("- Proven: " + provenLine(d, r.GeneratedAt) + "\n")

		if d.LastRun == nil {
			b.WriteString("\nNo runs recorded.\n")
			continue
		}
		run := d.LastRun
		fmt.Fprintf(&b, "\n### Last run — %s\n\n", runHeadline(run, r.GeneratedAt))
		if run.Snapshot != "" {
			fmt.Fprintf(&b, "Audited snapshot: `%s`\n\n", mdEscape(run.Snapshot))
		}
		if len(run.Steps) > 0 {
			b.WriteString("| Level | Status | Duration | Summary |\n|---|---|---|---|\n")
			for _, s := range run.Steps {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
					s.Kind, strings.ToUpper(s.Status), durationMS(s.DurationMS), mdEscape(s.Summary))
			}
		}
		if len(run.Evidence) > 0 {
			b.WriteString("\n| Check | Target | Expected | Actual | Status |\n|---|---|---|---|---|\n")
			for _, e := range run.Evidence {
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
					e.CheckKind, mdEscape(e.Target), mdEscape(e.Expected), mdEscape(e.Actual), statusCell(e))
			}
		}
	}
	return []byte(b.String())
}

// slaLine is the drill heading's verdict: within SLA, STALE, or no SLA set.
func slaLine(d *Drill) string {
	switch {
	case d.Stale && d.MaxProofAgeSecs > 0:
		return fmt.Sprintf("STALE (no %s proof within %s)", d.HeadlineLevel,
			humanDuration(time.Duration(d.MaxProofAgeSecs)*time.Second))
	case d.Stale:
		return "STALE"
	case d.MaxProofAgeSecs > 0:
		return "within SLA"
	default:
		return "no proof SLA set"
	}
}

func scheduleLine(d *Drill) string {
	s := "manual only"
	if d.Schedule != "" {
		s = d.Schedule
	}
	if d.MaxProofAgeSecs > 0 {
		s += " · proof SLA " + humanDuration(time.Duration(d.MaxProofAgeSecs)*time.Second)
	}
	return s
}

func provenLine(d *Drill, now time.Time) string {
	if len(d.Proofs) == 0 {
		return "never"
	}
	parts := make([]string, 0, len(d.Proofs))
	for _, p := range d.Proofs {
		parts = append(parts, fmt.Sprintf("%s %s (%s)", p.Level, ago(now, p.At), p.At.UTC().Format(dateLayout)))
	}
	return strings.Join(parts, " · ")
}

func runHeadline(run *Run, now time.Time) string {
	if run.Result == "" {
		return fmt.Sprintf("running (run %d, %s)", run.ID, run.Trigger)
	}
	s := fmt.Sprintf("%s (run %d, %s, %s, %s", strings.ToUpper(run.Result), run.ID, run.Trigger,
		ago(now, run.FinishedAt), durationMS(run.DurationMS))
	if run.BytesRestored > 0 || run.FilesRestored > 0 {
		s += fmt.Sprintf(", %s / %d files restored", humanBytes(run.BytesRestored), run.FilesRestored)
	}
	return s + ")"
}

func statusCell(e Evidence) string {
	s := strings.ToUpper(e.Status)
	if e.Weak {
		s += " (weak)"
	}
	return s
}

// mdEscape keeps evidence strings from breaking table cells.
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(s, "\n", " ")
}

// HTML renders the report as one self-contained page: inline CSS, no scripts,
// no external assets.
func HTML(r Report) ([]byte, error) {
	var b strings.Builder
	if err := htmlTmpl.Execute(&b, htmlData(r)); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// htmlView precomputes the strings the template shows, so the template stays
// free of logic and the Markdown/HTML renderers share one vocabulary.
type htmlView struct {
	GeneratedAt string
	Headline    string
	AllOK       bool
	Drills      []htmlDrill
}

type htmlDrill struct {
	Name     string
	SLA      string
	Stale    bool
	Source   string
	Schedule string
	Proven   string
	Run      *htmlRun
}

type htmlRun struct {
	Headline string
	Result   string
	Snapshot string
	Steps    []htmlStep
	Evidence []htmlEvidence
}

type htmlStep struct {
	Kind, Status, Duration, Summary string
}

type htmlEvidence struct {
	Check, Target, Expected, Actual, Status string
	Weak                                    bool
}

func htmlData(r Report) htmlView {
	v := htmlView{
		GeneratedAt: r.GeneratedAt.UTC().Format(dateLayout),
		Headline:    fmt.Sprintf("%d of %d datasets proven within SLA", r.ProvenOK, r.Total),
		AllOK:       r.ProvenOK == r.Total,
	}
	for i := range r.Drills {
		d := &r.Drills[i]
		hd := htmlDrill{
			Name:     d.Name,
			SLA:      slaLine(d),
			Stale:    d.Stale,
			Source:   fmt.Sprintf("%s (%s)", d.SourceName, d.SourceType),
			Schedule: scheduleLine(d),
			Proven:   provenLine(d, r.GeneratedAt),
		}
		if run := d.LastRun; run != nil {
			hr := htmlRun{Headline: runHeadline(run, r.GeneratedAt), Result: run.Result, Snapshot: run.Snapshot}
			for _, s := range run.Steps {
				hr.Steps = append(hr.Steps, htmlStep{
					Kind: s.Kind, Status: s.Status, Duration: durationMS(s.DurationMS), Summary: s.Summary,
				})
			}
			for _, e := range run.Evidence {
				hr.Evidence = append(hr.Evidence, htmlEvidence{
					Check: e.CheckKind, Target: e.Target, Expected: e.Expected,
					Actual: e.Actual, Status: e.Status, Weak: e.Weak,
				})
			}
			hd.Run = &hr
		}
		v.Drills = append(v.Drills, hd)
	}
	return v
}

// Status colors match the web UI: pass green, fail red, error amber,
// skipped gray — fail ≠ error, everywhere.
var htmlTmpl = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Redrill proof report</title>
<style>
body { font: 15px/1.5 system-ui, sans-serif; color: #1f2937; max-width: 60rem; margin: 2rem auto; padding: 0 1rem; }
h1 { font-size: 1.4rem; } h2 { font-size: 1.15rem; margin: 1.5rem 0 .25rem; } h3 { font-size: .95rem; }
table { border-collapse: collapse; margin: .5rem 0; width: 100%; }
th, td { border: 1px solid #d1d5db; padding: .25rem .5rem; text-align: left; font-size: .85rem; }
th { background: #f3f4f6; }
.muted { color: #6b7280; }
.banner { padding: .5rem .75rem; border-radius: .375rem; font-weight: 600; }
.banner.ok { background: #dcfce7; color: #166534; }
.banner.bad { background: #fee2e2; color: #991b1b; }
.sla-ok { color: #166534; } .sla-stale { color: #991b1b; font-weight: 600; }
.status-pass { color: #166534; font-weight: 600; }
.status-fail { color: #991b1b; font-weight: 600; }
.status-error { color: #92400e; font-weight: 600; }
.status-skipped { color: #6b7280; }
.weak { color: #6b7280; font-weight: 400; font-size: .8em; }
ul { margin: .25rem 0; padding-left: 1.25rem; }
</style>
</head>
<body>
<h1>Redrill proof report</h1>
<p class="muted">Generated {{.GeneratedAt}}.</p>
<p class="banner {{if .AllOK}}ok{{else}}bad{{end}}">{{.Headline}}.</p>
{{range .Drills}}
<h2>{{.Name}} — <span class="{{if .Stale}}sla-stale{{else}}sla-ok{{end}}">{{.SLA}}</span></h2>
<ul>
<li>Source: {{.Source}}</li>
<li>Schedule: {{.Schedule}}</li>
<li>Proven: {{.Proven}}</li>
</ul>
{{if .Run}}
<h3>Last run — <span class="status-{{.Run.Result}}">{{.Run.Headline}}</span></h3>
{{if .Run.Snapshot}}<p class="muted">Audited snapshot: {{.Run.Snapshot}}</p>
{{end}}
{{if .Run.Steps}}
<table>
<tr><th>Level</th><th>Status</th><th>Duration</th><th>Summary</th></tr>
{{range .Run.Steps}}<tr><td>{{.Kind}}</td><td class="status-{{.Status}}">{{.Status}}</td><td>{{.Duration}}</td><td>{{.Summary}}</td></tr>
{{end}}</table>
{{end}}
{{if .Run.Evidence}}
<table>
<tr><th>Check</th><th>Target</th><th>Expected</th><th>Actual</th><th>Status</th></tr>
{{range .Run.Evidence}}<tr><td>{{.Check}}</td><td>{{.Target}}</td><td>{{.Expected}}</td><td>{{.Actual}}</td><td class="status-{{.Status}}">{{.Status}}{{if .Weak}} <span class="weak">(weak)</span>{{end}}</td></tr>
{{end}}</table>
{{end}}
{{else}}
<p class="muted">No runs recorded.</p>
{{end}}
{{end}}
</body>
</html>
`))

// "3d ago"; a zero t reads as "never".
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "<1m ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// humanDuration renders an SLA in the config's own vocabulary (10d, 36h, 30m).
func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return d.String()
	}
}

func durationMS(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d <= 0:
		return "-"
	case d < time.Second:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// humanBytes is IEC, one decimal under 10 units.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	suffix := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}[exp]
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, suffix)
	}
	return fmt.Sprintf("%.0f %s", v, suffix)
}
