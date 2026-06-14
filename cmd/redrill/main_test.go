package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{
			name:       "no args prints usage to stderr",
			args:       nil,
			wantCode:   2,
			wantStderr: []string{"Usage:"},
		},
		{
			name:       "unknown command",
			args:       []string{"frobnicate"},
			wantCode:   2,
			wantStderr: []string{`unknown command "frobnicate"`, "Usage:"},
		},
		{
			name:       "help command",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: []string{"Usage:"},
		},
		{
			name:       "version human-readable",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: []string{"redrill dev", "commit none"},
		},
		{
			name:       "version unknown flag",
			args:       []string{"version", "--frob"},
			wantCode:   2,
			wantStderr: []string{"flag provided but not defined"},
		},
		{
			name:     "version help flag",
			args:     []string{"version", "-h"},
			wantCode: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			got := run(tt.args, &stdout, &stderr)
			if got != tt.wantCode {
				t.Errorf("run(%v) = %d, want %d (stderr: %q)", tt.args, got, tt.wantCode, stderr.String())
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout missing %q; got %q", want, stdout.String())
				}
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr missing %q; got %q", want, stderr.String())
				}
			}
		})
	}
}

func TestVersionJSON(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if got := run([]string{"version", "--json"}, &stdout, &stderr); got != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", got, stderr.String())
	}
	var info map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("output is not valid JSON: %v; got %q", err, stdout.String())
	}
	for _, key := range []string{"version", "commit", "date", "go"} {
		if info[key] == "" {
			t.Errorf("missing key %q in %v", key, info)
		}
	}
	if info["version"] != "dev" {
		t.Errorf("version = %q, want %q", info["version"], "dev")
	}
}

const validConfig = `
version: 1
data_dir: /v
scratch: {dir: /s}
sources: [{name: s, type: borg, repo: r}]
drills: [{name: d, source: s, schedule: "Sun 04:10", levels: {l1: {native_check: true}}}]
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidate(t *testing.T) {
	t.Parallel()
	t.Run("valid config exits 0", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		path := writeConfig(t, validConfig)
		if got := run([]string{"validate", "-c", path}, &stdout, &stderr); got != 0 {
			t.Fatalf("exit = %d, want 0 (stderr: %q)", got, stderr.String())
		}
		if !strings.Contains(stdout.String(), "is valid") {
			t.Errorf("stdout = %q, want 'is valid'", stdout.String())
		}
	})

	t.Run("invalid config exits 3", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		path := writeConfig(t, "version: 1\nscratch: {dir: /s}\n") // missing data_dir
		if got := run([]string{"validate", "-c", path}, &stdout, &stderr); got != 3 {
			t.Fatalf("exit = %d, want 3", got)
		}
		if !strings.Contains(stderr.String(), "data_dir") {
			t.Errorf("stderr = %q, want it to name the bad field", stderr.String())
		}
	})

	t.Run("missing file exits 3", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		if got := run([]string{"validate", "-c", "/no/such/config.yaml"}, &stdout, &stderr); got != 3 {
			t.Fatalf("exit = %d, want 3", got)
		}
	})

	t.Run("bad flag exits 2", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		if got := run([]string{"validate", "--frob"}, &stdout, &stderr); got != 2 {
			t.Fatalf("exit = %d, want 2", got)
		}
	})

	t.Run("json valid", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		path := writeConfig(t, validConfig)
		if got := run([]string{"validate", "-c", path, "--json"}, &stdout, &stderr); got != 0 {
			t.Fatalf("exit = %d, want 0", got)
		}
		var out struct {
			Valid bool `json:"valid"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v (%q)", err, stdout.String())
		}
		if !out.Valid {
			t.Errorf("valid = false, want true")
		}
	})

	t.Run("json invalid lists errors", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		path := writeConfig(t, "version: 9\ndata_dir: /v\nscratch: {dir: /s}\n")
		if got := run([]string{"validate", "-c", path, "--json"}, &stdout, &stderr); got != 3 {
			t.Fatalf("exit = %d, want 3", got)
		}
		var out struct {
			Valid  bool     `json:"valid"`
			Errors []string `json:"errors"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v (%q)", err, stdout.String())
		}
		if out.Valid || len(out.Errors) == 0 {
			t.Errorf("want valid=false with errors, got %+v", out)
		}
	})
}

// setupRunConfig writes a dumpdir with one gzip dump plus a config, returning the config path.
func setupRunConfig(t *testing.T, body string, mtime time.Time) string {
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
	if _, err := gz.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	body2 := fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources:
  - {name: dumps, type: dumpdir, path: %s, pattern: "*.sql.gz", pick: newest}
drills:
  - name: app-db
    source: dumps
    schedule: "Sun 05:00"
    levels:
      l1: {file_min_bytes: 1, compression_test: true, max_age: 36h}
`, filepath.Join(tmp, "data"), filepath.Join(tmp, "scratch"), dumps)
	return writeConfig(t, body2)
}

func TestRunDrill(t *testing.T) {
	t.Parallel()

	t.Run("healthy dump exits 0", func(t *testing.T) {
		t.Parallel()
		cfg := setupRunConfig(t, "SELECT 1;", time.Now().Add(-1*time.Hour))
		var stdout, stderr bytes.Buffer
		if got := run([]string{"run", "app-db", "-c", cfg}, &stdout, &stderr); got != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", got, stderr.String())
		}
		if !strings.Contains(stdout.String(), "PASS") {
			t.Errorf("stdout = %q, want PASS", stdout.String())
		}
	})

	t.Run("stale dump fails with exit 1", func(t *testing.T) {
		t.Parallel()
		cfg := setupRunConfig(t, "SELECT 1;", time.Now().Add(-30*24*time.Hour))
		var stdout, stderr bytes.Buffer
		if got := run([]string{"run", "app-db", "-c", cfg}, &stdout, &stderr); got != 1 {
			t.Fatalf("exit = %d, want 1 (drill fail)", got)
		}
	})

	t.Run("unreadable source errors with exit 2", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		cfg := writeConfig(t, fmt.Sprintf(`version: 1
data_dir: %s
scratch: {dir: %s}
sources: [{name: dumps, type: dumpdir, path: /no/such/dir, pattern: "*.sql.gz"}]
drills: [{name: app-db, source: dumps, schedule: "x", levels: {l1: {max_age: 36h}}}]
`, filepath.Join(tmp, "data"), filepath.Join(tmp, "scratch")))
		var stdout, stderr bytes.Buffer
		if got := run([]string{"run", "app-db", "-c", cfg}, &stdout, &stderr); got != 2 {
			t.Fatalf("exit = %d, want 2 (infra error, distinct from drill fail)", got)
		}
	})

	t.Run("unknown drill exits 2", func(t *testing.T) {
		t.Parallel()
		cfg := setupRunConfig(t, "x", time.Now())
		var stdout, stderr bytes.Buffer
		if got := run([]string{"run", "ghost", "-c", cfg}, &stdout, &stderr); got != 2 {
			t.Fatalf("exit = %d, want 2", got)
		}
	})

	t.Run("missing NAME exits 2", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		if got := run([]string{"run"}, &stdout, &stderr); got != 2 {
			t.Fatalf("exit = %d, want 2", got)
		}
	})

	t.Run("json output", func(t *testing.T) {
		t.Parallel()
		cfg := setupRunConfig(t, "SELECT 1;", time.Now().Add(-1*time.Hour))
		var stdout, stderr bytes.Buffer
		if got := run([]string{"run", "app-db", "-c", cfg, "--json"}, &stdout, &stderr); got != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", got, stderr.String())
		}
		var out struct {
			Status string `json:"status"`
			Levels []struct {
				Level  string `json:"level"`
				Checks []struct {
					Kind   string `json:"kind"`
					Status string `json:"status"`
				} `json:"checks"`
			} `json:"levels"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v (%q)", err, stdout.String())
		}
		if out.Status != "pass" || len(out.Levels) != 1 || len(out.Levels[0].Checks) != 3 {
			t.Errorf("json = %+v, want pass/l1/3 checks", out)
		}
	})
}
