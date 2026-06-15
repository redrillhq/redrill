package server

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func uiFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><div id=root></div>SPA-INDEX")},
		"assets/app.js": {Data: []byte("console.log('redrill')")},
	}
}

func newUIServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{
		Store: testStore(t), Config: testConfig(),
		Now: func() time.Time { return testNow }, UI: uiFS(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUIServesIndexAtRoot(t *testing.T) {
	t.Parallel()
	rec := do(t, newUIServer(t).Handler(), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "SPA-INDEX") {
		t.Errorf("body did not contain index marker: %q", rec.Body.String())
	}
}

func TestUIServesAsset(t *testing.T) {
	t.Parallel()
	rec := do(t, newUIServer(t).Handler(), http.MethodGet, "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("content-type = %q, want javascript", ct)
	}
	if !strings.Contains(rec.Body.String(), "redrill") {
		t.Errorf("asset body mismatch: %q", rec.Body.String())
	}
}

func TestUIFallsBackToIndexForUnknownPath(t *testing.T) {
	t.Parallel()
	// A client-routed deep link (or hard refresh) must resolve to the SPA shell,
	// not 404.
	rec := do(t, newUIServer(t).Handler(), http.MethodGet, "/drills/app-db")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "SPA-INDEX") {
		t.Errorf("SPA fallback did not serve index: %q", rec.Body.String())
	}
}

func TestUIDoesNotShadowAPIOrInfra(t *testing.T) {
	t.Parallel()
	h := newUIServer(t).Handler()
	// The catch-all GET / must not capture the specific API/infra routes.
	if c := do(t, h, http.MethodGet, "/api/v1/drills").Code; c != http.StatusOK {
		t.Errorf("/api/v1/drills with UI present = %d, want 200", c)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/drills"); !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("/api/v1/drills served non-JSON: %q", rec.Header().Get("Content-Type"))
	}
	if c := do(t, h, http.MethodGet, "/healthz").Code; c != http.StatusOK {
		t.Errorf("/healthz with UI present = %d, want 200", c)
	}
	if c := do(t, h, http.MethodGet, "/metrics").Code; c != http.StatusOK {
		t.Errorf("/metrics with UI present = %d, want 200", c)
	}
}

func TestNoUIRouteWhenUnset(t *testing.T) {
	t.Parallel()
	// The default test server has no UI; the SPA catch-all must not be registered.
	if c := do(t, newTestServer(t).Handler(), http.MethodGet, "/").Code; c != http.StatusNotFound {
		t.Errorf("root with no UI = %d, want 404", c)
	}
}

func TestNewRejectsUIWithoutIndex(t *testing.T) {
	t.Parallel()
	_, err := New(Options{
		Store: testStore(t), Config: testConfig(),
		UI: fstest.MapFS{"assets/app.js": {Data: []byte("x")}},
	})
	if err == nil {
		t.Fatal("want error when UI assets lack index.html")
	}
}
