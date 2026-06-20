// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/store"
)

func TestConfigFileDefault(t *testing.T) {
	t.Setenv("REDRILL_CONFIG", "")
	if got := configFileDefault(); got != defaultConfigPath {
		t.Errorf("unset REDRILL_CONFIG: got %q, want %q", got, defaultConfigPath)
	}
	t.Setenv("REDRILL_CONFIG", "/custom/redrill.yaml")
	if got := configFileDefault(); got != "/custom/redrill.yaml" {
		t.Errorf("set REDRILL_CONFIG: got %q, want /custom/redrill.yaml", got)
	}
}

func TestPrintDrillNames(t *testing.T) {
	t.Parallel()
	t.Run("lists names in config order", func(t *testing.T) {
		t.Parallel()
		var w bytes.Buffer
		cfg := &config.Config{Drills: []config.Drill{{Name: "app-db"}, {Name: "photos"}}}
		printDrillNames(&w, cfg, "/etc/redrill/config.yaml")
		if got, want := w.String(), "configured drills: app-db, photos\n"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("notes when none are configured", func(t *testing.T) {
		t.Parallel()
		var w bytes.Buffer
		printDrillNames(&w, &config.Config{}, "/etc/redrill/config.yaml")
		if got, want := w.String(), "no drills configured in /etc/redrill/config.yaml\n"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestSelectDrills(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Sources: []config.Source{{Name: "s"}},
		Drills:  []config.Drill{{Name: "a", Source: "s"}, {Name: "b", Source: "s"}},
	}
	empty := &config.Config{}
	tests := []struct {
		name      string
		cfg       *config.Config
		drillName string
		haveName  bool
		all       bool
		wantNames string // comma-joined
		wantInter bool
		wantCode  int
		wantErr   string
	}{
		{name: "all returns every drill", cfg: cfg, all: true, wantNames: "a,b"},
		{name: "all rejects a NAME", cfg: cfg, all: true, drillName: "a", haveName: true, wantCode: 2, wantErr: "takes no drill NAME"},
		{name: "all with no drills", cfg: empty, all: true, wantCode: 2, wantErr: "no drills configured"},
		{name: "known name", cfg: cfg, drillName: "b", haveName: true, wantNames: "b"},
		{name: "unknown name lists drills", cfg: cfg, drillName: "ghost", haveName: true, wantCode: 2, wantErr: "no drill named"},
		{name: "no name signals interactive", cfg: cfg, wantInter: true},
		{name: "no name, no drills requires a NAME", cfg: empty, wantCode: 2, wantErr: "requires a drill NAME"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			names, inter, code := selectDrills(&stderr, tt.cfg, "/c.yaml", tt.drillName, tt.haveName, tt.all)
			if got := strings.Join(names, ","); got != tt.wantNames {
				t.Errorf("names = %q, want %q", got, tt.wantNames)
			}
			if inter != tt.wantInter {
				t.Errorf("interactive = %v, want %v", inter, tt.wantInter)
			}
			if !tt.wantInter && code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantErr != "" && !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantErr)
			}
		})
	}
}

func TestPickDrill(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Drills: []config.Drill{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	tests := []struct {
		name     string
		input    string
		wantName string
		wantCode int
	}{
		{name: "valid selection", input: "2\n", wantName: "b"},
		{name: "first", input: "1\n", wantName: "a"},
		{name: "blank cancels cleanly", input: "\n"},
		{name: "eof cancels cleanly", input: ""},
		{name: "out of range", input: "9\n", wantCode: 2},
		{name: "not a number", input: "xyz\n", wantCode: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			name, code := pickDrill(strings.NewReader(tt.input), &out, cfg)
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if name == "" && code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

func TestWorseResult(t *testing.T) {
	t.Parallel()
	p, f, e := store.ResultPass, store.ResultFail, store.ResultError
	cases := []struct{ a, b, want store.Result }{
		{p, p, p}, {p, f, f}, {f, p, f}, {p, e, e}, {e, p, e},
		{f, e, f}, {e, f, f}, {f, f, f}, {e, e, e}, // fail outranks error
	}
	for _, c := range cases {
		if got := worseResult(c.a, c.b); got != c.want {
			t.Errorf("worseResult(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPrintConfigError(t *testing.T) {
	t.Parallel()
	notExist := func(path string) error { return fmt.Errorf("read config %s: %w", path, os.ErrNotExist) }
	tests := []struct {
		name    string
		path    string
		err     error
		want    []string
		notWant []string
	}{
		{
			name: "missing default file names it and points at the override",
			path: defaultConfigPath,
			err:  notExist(defaultConfigPath),
			want: []string{"no config file at " + defaultConfigPath, "the default path", "-c <file>", "$REDRILL_CONFIG"},
		},
		{
			name:    "missing explicit file is reported without the default hint",
			path:    "/custom/redrill.yaml",
			err:     notExist("/custom/redrill.yaml"),
			want:    []string{"no config file at /custom/redrill.yaml"},
			notWant: []string{"default", "REDRILL_CONFIG"},
		},
		{
			name:    "a malformed file keeps the is-invalid detail",
			path:    defaultConfigPath,
			err:     errors.New("data_dir is required"),
			want:    []string{"is invalid", "data_dir is required"},
			notWant: []string{"no config file"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			printConfigError(&stderr, tt.path, tt.err)
			got := stderr.String()
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("stderr = %q, want it to contain %q", got, w)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("stderr = %q, want it NOT to contain %q", got, nw)
				}
			}
		})
	}
}

// setupStatusConfig writes a config with one dumpdir drill (L1, max_proof_age
// 10d), returning the config path and its data_dir so a test can pre-seed the store.
func setupStatusConfig(t *testing.T) (cfgPath, dataDir string) {
	t.Helper()
	tmp := t.TempDir()
	dataDir = filepath.Join(tmp, "data")
	body := fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources:
  - {name: dumps, type: dumpdir, path: %s, pattern: "*.sql.gz"}
drills:
  - name: app-db
    source: dumps
    schedule: "Sun 05:00"
    max_proof_age: 10d
    levels:
      l1: {file_min_bytes: 1, max_age: 36h}
`, dataDir, filepath.Join(tmp, "scratch"), filepath.Join(tmp, "dumps"))
	return writeConfig(t, body), dataDir
}

// withStore opens the store at dataDir, runs fn, and closes it, so no open
// handle races the command under test.
func withStore(t *testing.T, dataDir string, fn func(*store.Store)) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(dataDir, "redrill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	fn(st)
}

func recordRun(t *testing.T, st *store.Store, drill string, result store.Result, started time.Time, level string) {
	t.Helper()
	ctx := context.Background()
	id, err := st.CreateRun(ctx, store.Run{Drill: drill, Trigger: store.TriggerSchedule, StartedAt: started, Executor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	fin := store.Run{ID: id, Result: result, LevelReached: level, FinishedAt: started.Add(time.Second), DurationMS: 1200}
	if err := st.FinishRun(ctx, fin); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBadSchedule(t *testing.T) {
	t.Parallel()
	cfg := writeConfig(t, `version: 1
data_dir: /v
scratch: {dir: /s}
sources: [{name: s, type: dumpdir, path: /p, pattern: "*.gz"}]
drills: [{name: d, source: s, schedule: "not a schedule", levels: {l1: {max_age: 36h}}}]
`)
	var out, errb bytes.Buffer
	if code := run([]string{"validate", "-c", cfg}, &out, &errb); code != 3 {
		t.Fatalf("exit = %d, want 3 (bad schedule is a config error)", code)
	}
	if !strings.Contains(errb.String(), "schedule") {
		t.Errorf("stderr should name the schedule problem, got %q", errb.String())
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	t.Run("never proven is stale", func(t *testing.T) {
		t.Parallel()
		cfg, _ := setupStatusConfig(t)
		var out, errb bytes.Buffer
		if code := run([]string{"status", "-c", cfg}, &out, &errb); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb.String())
		}
		s := out.String()
		if !strings.Contains(s, "app-db") || !strings.Contains(s, "STALE") {
			t.Errorf("want app-db shown STALE, got:\n%s", s)
		}
		if !strings.Contains(s, "0 of 1 drills proven within SLA") {
			t.Errorf("want SLA footer, got:\n%s", s)
		}
	})

	t.Run("fresh proof is within SLA", func(t *testing.T) {
		t.Parallel()
		cfg, dataDir := setupStatusConfig(t)
		withStore(t, dataDir, func(st *store.Store) {
			if err := st.RecordProof(context.Background(), "app-db", "l1", now.Add(-24*time.Hour)); err != nil {
				t.Fatal(err)
			}
		})
		var out, errb bytes.Buffer
		if code := run([]string{"status", "-c", cfg}, &out, &errb); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb.String())
		}
		s := out.String()
		if strings.Contains(s, "STALE") {
			t.Errorf("a 1-day-old proof under a 10d SLA must not be STALE:\n%s", s)
		}
		if !strings.Contains(s, "1 of 1 drills proven within SLA") {
			t.Errorf("want all-proven footer, got:\n%s", s)
		}
	})

	t.Run("old proof past SLA is stale", func(t *testing.T) {
		t.Parallel()
		cfg, dataDir := setupStatusConfig(t)
		withStore(t, dataDir, func(st *store.Store) {
			if err := st.RecordProof(context.Background(), "app-db", "l1", now.Add(-30*24*time.Hour)); err != nil {
				t.Fatal(err)
			}
		})
		var out, errb bytes.Buffer
		if code := run([]string{"status", "-c", cfg}, &out, &errb); code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if !strings.Contains(out.String(), "STALE") {
			t.Errorf("a 30-day-old proof under a 10d SLA must be STALE:\n%s", out.String())
		}
	})

	t.Run("shows last run result", func(t *testing.T) {
		t.Parallel()
		cfg, dataDir := setupStatusConfig(t)
		withStore(t, dataDir, func(st *store.Store) {
			recordRun(t, st, "app-db", store.ResultFail, now.Add(-time.Hour), "l1")
		})
		var out, errb bytes.Buffer
		if code := run([]string{"status", "-c", cfg}, &out, &errb); code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if !strings.Contains(out.String(), "fail") {
			t.Errorf("want last run 'fail' shown, got:\n%s", out.String())
		}
	})
}

func TestStatusJSON(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cfg, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		if err := st.RecordProof(context.Background(), "app-db", "l1", now.Add(-30*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	})
	var out, errb bytes.Buffer
	if code := run([]string{"status", "-c", cfg, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb.String())
	}
	var arr []map[string]any
	if err := json.Unmarshal(out.Bytes(), &arr); err != nil {
		t.Fatalf("bad json: %v (%q)", err, out.String())
	}
	if len(arr) != 1 {
		t.Fatalf("want 1 drill, got %d", len(arr))
	}
	got := arr[0]
	if got["drill"] != "app-db" || got["headline_level"] != "l1" {
		t.Errorf("drill/headline = %v/%v, want app-db/l1", got["drill"], got["headline_level"])
	}
	if stale, _ := got["stale"].(bool); !stale {
		t.Errorf("stale = %v, want true", got["stale"])
	}
	if _, ok := got["last_proven"]; !ok {
		t.Errorf("want last_proven present, got %v", got)
	}
	if _, ok := got["next_run"]; !ok {
		t.Errorf("want next_run present, got %v", got)
	}
}

func TestHistory(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	t.Run("lists runs newest first", func(t *testing.T) {
		t.Parallel()
		cfg, dataDir := setupStatusConfig(t)
		withStore(t, dataDir, func(st *store.Store) {
			recordRun(t, st, "app-db", store.ResultPass, now.Add(-2*time.Hour), "l1")
			recordRun(t, st, "app-db", store.ResultFail, now.Add(-time.Hour), "l1")
		})
		var out, errb bytes.Buffer
		if code := run([]string{"history", "app-db", "-c", cfg}, &out, &errb); code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb.String())
		}
		s := out.String()
		if !strings.Contains(s, "RESULT") {
			t.Errorf("want table header, got:\n%s", s)
		}
		if !strings.Contains(s, "pass") || !strings.Contains(s, "fail") {
			t.Errorf("want both run results, got:\n%s", s)
		}
	})

	t.Run("no runs", func(t *testing.T) {
		t.Parallel()
		cfg, _ := setupStatusConfig(t)
		var out, errb bytes.Buffer
		if code := run([]string{"history", "app-db", "-c", cfg}, &out, &errb); code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if !strings.Contains(out.String(), "no runs recorded for app-db") {
			t.Errorf("want empty-history message, got %q", out.String())
		}
	})

	t.Run("missing NAME exits 2", func(t *testing.T) {
		t.Parallel()
		cfg, _ := setupStatusConfig(t)
		var out, errb bytes.Buffer
		if code := run([]string{"history", "-c", cfg}, &out, &errb); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
	})
}

func TestHistoryJSONLimit(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cfg, dataDir := setupStatusConfig(t)
	withStore(t, dataDir, func(st *store.Store) {
		for i := range 3 {
			recordRun(t, st, "app-db", store.ResultPass, now.Add(-time.Duration(i+1)*time.Hour), "l1")
		}
	})
	var out, errb bytes.Buffer
	if code := run([]string{"history", "app-db", "-c", cfg, "--json", "-n", "2"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb.String())
	}
	var arr []map[string]any
	if err := json.Unmarshal(out.Bytes(), &arr); err != nil {
		t.Fatalf("bad json: %v (%q)", err, out.String())
	}
	if len(arr) != 2 {
		t.Fatalf("want 2 runs (limit -n 2), got %d", len(arr))
	}
	// ListRuns orders by id desc, so the first id is the largest.
	if id0, id1 := arr[0]["id"].(float64), arr[1]["id"].(float64); id0 <= id1 {
		t.Errorf("want newest first (id %v > %v)", id0, id1)
	}
}

func TestServeErrors(t *testing.T) {
	t.Parallel()

	t.Run("bad config exits 3", func(t *testing.T) {
		t.Parallel()
		cfg := writeConfig(t, "version: 1\nscratch: {dir: /s}\n") // missing data_dir
		var out, errb bytes.Buffer
		if code := run([]string{"serve", "-c", cfg}, &out, &errb); code != 3 {
			t.Fatalf("exit = %d, want 3", code)
		}
	})

	t.Run("bad schedule exits 3", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		cfg := writeConfig(t, fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources: [{name: dumps, type: dumpdir, path: /tmp, pattern: "*.gz"}]
drills: [{name: d, source: dumps, schedule: "nope", levels: {l1: {max_age: 36h}}}]
`, filepath.Join(tmp, "data"), filepath.Join(tmp, "scratch")))
		var out, errb bytes.Buffer
		if code := run([]string{"serve", "-c", cfg}, &out, &errb); code != 3 {
			t.Fatalf("exit = %d, want 3 (stderr %q)", code, errb.String())
		}
	})
}

// TestServeStartStop: serve boots the store + scheduler and returns 0 on cancel.
// The cancel waits for the store file so it can't race startup.
func TestServeStartStop(t *testing.T) {
	cfgPath, dataDir := setupStatusConfig(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- serve(ctx, cfg, log) }()

	waitForFile(t, filepath.Join(dataDir, "redrill.db"), 10*time.Second)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve exit = %d, want 0 on clean shutdown", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop after context cancel")
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("file %s did not appear within %v", path, timeout)
}
