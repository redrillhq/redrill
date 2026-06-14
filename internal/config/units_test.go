package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"30m", 30 * time.Minute, false},
		{"36h", 36 * time.Hour, false},
		{"8d", 8 * 24 * time.Hour, false},
		{"0d", 0, false},
		{"90s", 90 * time.Second, false},
		{" 2h ", 2 * time.Hour, false},
		{"", 0, true},
		{"8days", 0, true},
		{"d", 0, true},
		{"-5m", 0, true},
		{"-3d", 0, true},
		{"banana", 0, true},
		{"10", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseDuration(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseDuration(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"1MiB", 1 << 20, false},
		{"50GiB", 50 << 30, false},
		{"40MiB", 40 << 20, false},
		{"1KiB", 1024, false},
		{"1B", 1, false},
		{"2KB", 2000, false},
		{"1024", 1024, false},
		{"1.5GiB", int64(1.5 * (1 << 30)), false},
		{"", 0, true},
		{"5Gigs", 0, true},
		{"-1MiB", 0, true},
		{"MiB", 0, true},
		{"banana", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseSize(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseSize(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestDurationUnmarshalYAML(t *testing.T) {
	t.Parallel()
	var d Duration
	if err := yaml.Unmarshal([]byte("36h"), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Duration() != 36*time.Hour {
		t.Errorf("got %v, want 36h", d.Duration())
	}
	if err := yaml.Unmarshal([]byte("nope"), &d); err == nil {
		t.Error("want error for bad duration")
	}
}

func TestSizeUnmarshalYAML(t *testing.T) {
	t.Parallel()
	var s Size
	if err := yaml.Unmarshal([]byte("50GiB"), &s); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if s.Bytes() != 50<<30 {
		t.Errorf("got %d, want %d", s.Bytes(), int64(50)<<30)
	}
	if err := yaml.Unmarshal([]byte("4096"), &s); err != nil {
		t.Fatalf("unmarshal int: %v", err)
	}
	if s.Bytes() != 4096 {
		t.Errorf("got %d, want 4096", s.Bytes())
	}
}
