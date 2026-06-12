package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
			wantStdout: []string{"drillbit dev", "commit none"},
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
