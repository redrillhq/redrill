package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
)

// freeAddr binds an ephemeral port, then frees it so the daemon can claim it.
func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// setupServeConfig writes a runnable L1 dumpdir drill plus an HTTP server bound to
// addr and a healthchecks ping URL, returning the config path.
func setupServeConfig(t *testing.T, addr, healthchecksURL string) string {
	t.Helper()
	tmp := t.TempDir()
	dumps := filepath.Join(tmp, "dumps")
	if err := os.Mkdir(dumps, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dumps, "app-1.sql.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte("SELECT 1;")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
server: {listen: %q, allow_no_auth: true}
notify: {healthchecks_url: %q}
sources:
  - {name: dumps, type: dumpdir, path: %s, pattern: "*.sql.gz", pick: newest}
drills:
  - name: app-db
    source: dumps
    schedule: "Sun 05:00"
    levels:
      l1: {file_min_bytes: 1, compression_test: true, max_age: 36h}
`, filepath.Join(tmp, "data"), filepath.Join(tmp, "scratch"), addr, healthchecksURL, dumps)
	return writeConfig(t, body)
}

func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func waitForHealth(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/healthz", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("http server never became healthy")
}

func TestServeHTTPEndpoints(t *testing.T) {
	addr := freeAddr(t)
	base := "http://" + addr

	pinged := make(chan struct{}, 8)
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case pinged <- struct{}{}:
		default:
		}
	}))
	defer hc.Close()

	cfgPath := setupServeConfig(t, addr, hc.URL)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- serve(ctx, cfg, log) }()
	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("serve exit = %d, want 0", code)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("serve did not stop after cancel")
		}
	}()

	waitForHealth(t, base)

	// The dead-man heartbeat fires on the startup scheduler cycle.
	select {
	case <-pinged:
	case <-time.After(5 * time.Second):
		t.Error("healthchecks URL was never pinged")
	}

	if code, body := httpGet(t, base+"/api/v1/drills"); code != http.StatusOK || !strings.Contains(body, "app-db") {
		t.Fatalf("/api/v1/drills = %d %q", code, body)
	}
	if code, body := httpGet(t, base+"/metrics"); code != http.StatusOK || !strings.Contains(body, "redrill_proof_sla_ok") {
		t.Fatalf("/metrics = %d %q", code, body)
	}
	// The embedded SPA is served at / (and as the fallback for client routes).
	if code, body := httpGet(t, base+"/"); code != http.StatusOK || !strings.Contains(body, `id="root"`) {
		t.Fatalf("GET / (web UI) = %d %q", code, body)
	}

	// Trigger a run, then wait for it to land with trigger "api".
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/api/v1/drills/app-db/run", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST run = %d, want 202", resp.StatusCode)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("API-triggered run never appeared")
		}
		code, body := httpGet(t, base+"/api/v1/drills/app-db/runs")
		if code != http.StatusOK {
			t.Fatalf("/runs = %d", code)
		}
		var runs []map[string]any
		if err := json.Unmarshal([]byte(body), &runs); err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 && runs[0]["trigger"] == "api" {
			switch r := runs[0]["result"]; r {
			case "pass":
				return // the async API-triggered run completed and passed
			case "", nil:
				// still running; keep polling
			default:
				t.Fatalf("triggered run result = %v, want pass", r)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPingHealthchecks(t *testing.T) {
	t.Parallel()
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit <- struct{}{}
	}))
	defer srv.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pingHealthchecks(context.Background(), srv.URL, log)
	select {
	case <-hit:
	case <-time.After(3 * time.Second):
		t.Fatal("pingHealthchecks did not reach the server")
	}
	// A bad URL must not panic or block.
	pingHealthchecks(context.Background(), "http://127.0.0.1:1", log)
}
