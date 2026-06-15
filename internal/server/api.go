package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/scheduler"
	"github.com/alyamovsky/redrill/internal/store"
)

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// drillView is a drill's computed status: last run, proof age per level, next
// run, and SLA state — the same picture `redrill status` renders.
type drillView struct {
	Drill           string            `json:"drill"`
	Source          string            `json:"source"`
	HeadlineLevel   string            `json:"headline_level,omitempty"`
	Stale           bool              `json:"stale"`
	MaxProofAgeSecs int64             `json:"max_proof_age_seconds,omitempty"`
	LastResult      string            `json:"last_result,omitempty"`
	LevelReached    string            `json:"level_reached,omitempty"`
	LastRunAt       string            `json:"last_run_at,omitempty"`
	LastProven      string            `json:"last_proven,omitempty"`
	NextRun         string            `json:"next_run,omitempty"`
	Proofs          map[string]string `json:"proofs,omitempty"`
}

func (s *Server) handleDrills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.now()
	out := make([]drillView, 0, len(s.cfg.Drills))
	for i := range s.cfg.Drills {
		v, err := s.drillView(ctx, &s.cfg.Drills[i], now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) drillView(ctx context.Context, d *config.Drill, now time.Time) (drillView, error) {
	v := drillView{
		Drill: d.Name, Source: d.Source, HeadlineLevel: scheduler.HeadlineLevel(*d),
		MaxProofAgeSecs: int64(d.MaxProofAge.Duration().Seconds()),
	}

	runs, err := s.store.ListRuns(ctx, d.Name, 1)
	if err != nil {
		return drillView{}, err
	}
	if len(runs) > 0 {
		last := runs[0]
		v.LastResult = string(last.Result)
		v.LevelReached = last.LevelReached
		if !last.FinishedAt.IsZero() {
			v.LastRunAt = last.FinishedAt.Format(time.RFC3339)
		}
	}

	proofs, err := s.store.ListProofs(ctx, d.Name)
	if err != nil {
		return drillView{}, err
	}
	if len(proofs) > 0 {
		v.Proofs = make(map[string]string, len(proofs))
		for _, p := range proofs {
			v.Proofs[p.Level] = p.LastProvenAt.Format(time.RFC3339)
		}
	}

	var headlineProof time.Time
	if v.HeadlineLevel != "" {
		at, ok, err := s.store.GetProof(ctx, d.Name, v.HeadlineLevel)
		if err != nil {
			return drillView{}, err
		}
		if ok {
			headlineProof = at
			v.LastProven = at.Format(time.RFC3339)
		}
	}
	v.Stale = scheduler.Stale(d.MaxProofAge.Duration(), headlineProof, now)
	if sched, err := scheduler.ParseSchedule(d.Schedule); err == nil {
		v.NextRun = sched.Next(now).Format(time.RFC3339)
	}
	return v, nil
}

// runView mirrors `redrill history`: a run's verdict, trigger, timing, and bytes.
type runView struct {
	ID            int64  `json:"id"`
	Drill         string `json:"drill"`
	Result        string `json:"result"`
	Trigger       string `json:"trigger"`
	LevelReached  string `json:"level_reached"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at,omitempty"`
	DurationMS    int64  `json:"duration_ms"`
	BytesRestored int64  `json:"bytes_restored"`
	FilesRestored int64  `json:"files_restored"`
}

func toRunView(r store.Run) runView {
	v := runView{
		ID: r.ID, Drill: r.Drill, Result: string(r.Result), Trigger: string(r.Trigger),
		LevelReached: r.LevelReached, StartedAt: r.StartedAt.Format(time.RFC3339),
		DurationMS: r.DurationMS, BytesRestored: r.BytesRestored, FilesRestored: r.FilesRestored,
	}
	if !r.FinishedAt.IsZero() {
		v.FinishedAt = r.FinishedAt.Format(time.RFC3339)
	}
	return v
}

func (s *Server) handleDrillRuns(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.drillExists(name) {
		writeError(w, http.StatusNotFound, "no such drill")
		return
	}
	limit := 20
	if q := r.URL.Query().Get("n"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "n must be a non-negative integer")
			return
		}
		limit = n
	}
	runs, err := s.store.ListRuns(r.Context(), name, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]runView, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunView(run))
	}
	writeJSON(w, http.StatusOK, out)
}

type stepView struct {
	Idx        int    `json:"idx"`
	Kind       string `json:"kind"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Status     string `json:"status"`
	Summary    string `json:"summary,omitempty"`
}

type evidenceView struct {
	Idx       int    `json:"idx"`
	CheckKind string `json:"check_kind"`
	Target    string `json:"target,omitempty"`
	Expected  string `json:"expected,omitempty"`
	Actual    string `json:"actual,omitempty"`
	Status    string `json:"status"`
	Weak      bool   `json:"weak,omitempty"`
}

// artifactView exposes redacted-log metadata. The on-disk path is intentionally
// not served (it is host-local; no artifact-download route exists in v1).
type artifactView struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type runDetail struct {
	runView
	Steps     []stepView     `json:"steps"`
	Evidence  []evidenceView `json:"evidence"`
	Artifacts []artifactView `json:"artifacts"`
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "run id must be a positive integer")
		return
	}
	ctx := r.Context()
	run, err := s.store.GetRun(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such run")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	detail := runDetail{runView: toRunView(run), Steps: []stepView{}, Evidence: []evidenceView{}, Artifacts: []artifactView{}}
	steps, err := s.store.ListSteps(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, st := range steps {
		sv := stepView{Idx: st.Idx, Kind: st.Kind, StartedAt: st.StartedAt.Format(time.RFC3339), Status: st.Status, Summary: st.Summary}
		if !st.FinishedAt.IsZero() {
			sv.FinishedAt = st.FinishedAt.Format(time.RFC3339)
		}
		detail.Steps = append(detail.Steps, sv)
	}
	evs, err := s.store.ListEvidence(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range evs {
		detail.Evidence = append(detail.Evidence, evidenceView{
			Idx: e.Idx, CheckKind: e.CheckKind, Target: e.Target,
			Expected: e.Expected, Actual: e.Actual, Status: e.Status, Weak: e.Weak,
		})
	}
	arts, err := s.store.ListArtifacts(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, a := range arts {
		detail.Artifacts = append(detail.Artifacts, artifactView{Name: a.Name, Bytes: a.Bytes})
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleTrigger(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.drillExists(name) {
		writeError(w, http.StatusNotFound, "no such drill")
		return
	}
	if s.trigger == nil {
		writeError(w, http.StatusServiceUnavailable, "triggering is disabled")
		return
	}
	switch err := s.trigger(name); {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]string{"drill": name, "status": "started"})
	case errors.Is(err, ErrBusy):
		writeError(w, http.StatusConflict, "a run is already in flight")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) drillExists(name string) bool {
	for i := range s.cfg.Drills {
		if s.cfg.Drills[i].Name == name {
			return true
		}
	}
	return false
}
