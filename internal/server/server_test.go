package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/store"
)

var testNow = time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// testConfig has one L1 drill "app-db" with a 10-day Proof SLA.
func testConfig() *config.Config {
	on := true
	return &config.Config{
		Version: 1, DataDir: "/tmp", Concurrency: 1,
		Scratch: config.Scratch{Dir: ""},
		Drills: []config.Drill{{
			Name: "app-db", Source: "pg-dumps", Schedule: "Sun 04:10",
			MaxProofAge: config.Duration(10 * 24 * time.Hour),
			Levels:      config.Levels{L1: &config.L1{CompressionTest: &on}},
		}},
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{Store: testStore(t), Config: testConfig(), Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// seedFinishedRun creates and finishes a run, returning its id.
func seedFinishedRun(t *testing.T, st *store.Store, drill string, result store.Result, at time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := st.CreateRun(ctx, store.Run{Drill: drill, Trigger: store.TriggerSchedule, StartedAt: at, Executor: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, store.Run{
		ID: id, Result: result, LevelReached: "l1", BytesRestored: 1000, FilesRestored: 5,
		DurationMS: 1500, FinishedAt: at.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func do(t *testing.T, h http.Handler, method, path string, auth ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if len(auth) == 2 {
		req.SetBasicAuth(auth[0], auth[1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T: %v (body %q)", v, err, rec.Body.String())
	}
	return v
}

func TestHealthz(t *testing.T) {
	t.Parallel()
	rec := do(t, newTestServer(t).Handler(), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[map[string]string](t, rec)
	if got["status"] != "ok" {
		t.Errorf("body = %v, want status ok", got)
	}
}

func TestDrillsList(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// A fresh proof keeps the drill within its 10-day SLA.
	seedFinishedRun(t, s.store, "app-db", store.ResultPass, testNow.Add(-time.Hour))
	if err := s.store.RecordProof(context.Background(), "app-db", "l1", testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s.Handler(), http.MethodGet, "/api/v1/drills")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[[]drillView](t, rec)
	if len(got) != 1 {
		t.Fatalf("drills = %d, want 1", len(got))
	}
	d := got[0]
	if d.Drill != "app-db" || d.Source != "pg-dumps" || d.HeadlineLevel != "l1" {
		t.Errorf("drill view = %+v", d)
	}
	if d.Stale {
		t.Error("drill should be within SLA (fresh proof), got stale")
	}
	if d.LastResult != "pass" || d.LastProven == "" || d.NextRun == "" {
		t.Errorf("missing computed fields: %+v", d)
	}
	if d.Proofs["l1"] == "" {
		t.Errorf("proofs map missing l1: %+v", d.Proofs)
	}
}

func TestDrillsListStaleWhenNeverProven(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rec := do(t, s.Handler(), http.MethodGet, "/api/v1/drills")
	got := decode[[]drillView](t, rec)
	if len(got) != 1 || !got[0].Stale {
		t.Fatalf("never-proven drill must be stale: %+v", got)
	}
	if got[0].LastProven != "" {
		t.Errorf("never proven, want empty last_proven: %q", got[0].LastProven)
	}
}

func TestDrillRuns(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	seedFinishedRun(t, s.store, "app-db", store.ResultPass, testNow.Add(-2*time.Hour))
	seedFinishedRun(t, s.store, "app-db", store.ResultFail, testNow.Add(-time.Hour))

	rec := do(t, s.Handler(), http.MethodGet, "/api/v1/drills/app-db/runs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	runs := decode[[]runView](t, rec)
	if len(runs) != 2 || runs[0].Result != "fail" || runs[1].Result != "pass" {
		t.Fatalf("runs newest-first mismatch: %+v", runs)
	}

	if c := do(t, s.Handler(), http.MethodGet, "/api/v1/drills/ghost/runs").Code; c != http.StatusNotFound {
		t.Errorf("unknown drill status = %d, want 404", c)
	}
	if c := do(t, s.Handler(), http.MethodGet, "/api/v1/drills/app-db/runs?n=-1").Code; c != http.StatusBadRequest {
		t.Errorf("bad n status = %d, want 400", c)
	}
	if c := do(t, s.Handler(), http.MethodGet, "/api/v1/drills/app-db/runs?n=abc").Code; c != http.StatusBadRequest {
		t.Errorf("non-numeric n status = %d, want 400", c)
	}
}

func TestRunDetail(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	ctx := context.Background()
	id := seedFinishedRun(t, s.store, "app-db", store.ResultFail, testNow.Add(-time.Hour))
	if err := s.store.AddStep(ctx, store.RunStep{RunID: id, Kind: "l1", StartedAt: testNow, Status: "fail", Summary: "L1 failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddEvidence(ctx, store.Evidence{RunID: id, CheckKind: "compression_test", Target: "dump.gz", Expected: "valid gzip", Actual: "corrupt", Status: "fail"}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddArtifact(ctx, store.Artifact{RunID: id, Name: "run.log", Path: "/secret/local/path.log", Bytes: 42}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, s.Handler(), http.MethodGet, "/api/v1/runs/"+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[runDetail](t, rec)
	if got.ID != id || got.Result != "fail" {
		t.Errorf("run view mismatch: %+v", got.runView)
	}
	if len(got.Steps) != 1 || got.Steps[0].Kind != "l1" {
		t.Errorf("steps mismatch: %+v", got.Steps)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].CheckKind != "compression_test" || got.Evidence[0].Status != "fail" {
		t.Errorf("evidence mismatch: %+v", got.Evidence)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].Name != "run.log" || got.Artifacts[0].Bytes != 42 {
		t.Errorf("artifacts mismatch: %+v", got.Artifacts)
	}
	// The host-local artifact path must never be exposed.
	if body := rec.Body.String(); strings.Contains(body, "/secret/local/path.log") {
		t.Error("artifact on-disk path leaked into the API response")
	}

	if c := do(t, s.Handler(), http.MethodGet, "/api/v1/runs/99999").Code; c != http.StatusNotFound {
		t.Errorf("unknown run status = %d, want 404", c)
	}
	if c := do(t, s.Handler(), http.MethodGet, "/api/v1/runs/abc").Code; c != http.StatusBadRequest {
		t.Errorf("non-numeric run id status = %d, want 400", c)
	}
}

func TestTrigger(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	var triggered string
	s.trigger = func(name string) error { triggered = name; return nil }

	rec := do(t, s.Handler(), http.MethodPost, "/api/v1/drills/app-db/run")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if triggered != "app-db" {
		t.Errorf("trigger called with %q, want app-db", triggered)
	}

	// Busy → 409.
	s.trigger = func(string) error { return ErrBusy }
	if c := do(t, s.Handler(), http.MethodPost, "/api/v1/drills/app-db/run").Code; c != http.StatusConflict {
		t.Errorf("busy status = %d, want 409", c)
	}
	// Unknown drill → 404 (before the trigger is consulted).
	if c := do(t, s.Handler(), http.MethodPost, "/api/v1/drills/ghost/run").Code; c != http.StatusNotFound {
		t.Errorf("unknown drill status = %d, want 404", c)
	}
	// Disabled trigger → 503.
	s.trigger = nil
	if c := do(t, s.Handler(), http.MethodPost, "/api/v1/drills/app-db/run").Code; c != http.StatusServiceUnavailable {
		t.Errorf("nil trigger status = %d, want 503", c)
	}
}

func TestTriggerRateLimited(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.trigger = func(string) error { return nil }
	s.limiter = rate.NewLimiter(0, 0) // deny everything
	if c := do(t, s.Handler(), http.MethodPost, "/api/v1/drills/app-db/run").Code; c != http.StatusTooManyRequests {
		t.Errorf("rate-limited status = %d, want 429", c)
	}
}

func TestBasicAuth(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	s.auth = &basicAuth{users: map[string][]byte{"admin": hash}}
	h := s.Handler()

	// API requires credentials.
	if c := do(t, h, http.MethodGet, "/api/v1/drills").Code; c != http.StatusUnauthorized {
		t.Errorf("no-creds API status = %d, want 401", c)
	}
	if c := do(t, h, http.MethodGet, "/api/v1/drills", "admin", "wrong").Code; c != http.StatusUnauthorized {
		t.Errorf("bad-creds API status = %d, want 401", c)
	}
	if c := do(t, h, http.MethodGet, "/api/v1/drills", "admin", "s3cret").Code; c != http.StatusOK {
		t.Errorf("good-creds API status = %d, want 200", c)
	}
	// Infra endpoints stay open.
	if c := do(t, h, http.MethodGet, "/healthz").Code; c != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 (no auth)", c)
	}
	if c := do(t, h, http.MethodGet, "/metrics").Code; c != http.StatusOK {
		t.Errorf("metrics status = %d, want 200 (no auth)", c)
	}
}
