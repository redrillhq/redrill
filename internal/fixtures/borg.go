package fixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RequireBorg skips the test unless the borg binary is on PATH.
func RequireBorg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("borg"); err != nil {
		t.Skip("borg not installed; provide borg to run engine fixtures")
	}
}

type borgSpec struct {
	archiveAge time.Duration
	omitData   bool
}

type BorgOption func(*borgSpec)

// BorgArchiveAge backdates the archive by d via borg create --timestamp (a stale
// source).
func BorgArchiveAge(d time.Duration) BorgOption {
	return func(s *borgSpec) { s.archiveAge = d }
}

// BorgOmitData drops the data/ tree from the archive (models a bad exclude /
// missing-data-dir).
func BorgOmitData() BorgOption {
	return func(s *borgSpec) { s.omitData = true }
}

// Borg builds an encrypted borg repo with a config/ tree (and, unless
// BorgOmitData, a data/ tree) and one archive "arch1", returning the repo path
// and the passphrase file. Requires borg on PATH — gate with RequireBorg.
func Borg(t *testing.T, opts ...BorgOption) (repo, passFile string) {
	t.Helper()
	var s borgSpec
	for _, o := range opts {
		o(&s)
	}

	dir := t.TempDir()
	repo = filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	mkdirAll(t, filepath.Join(src, "config"))
	writeBytes(t, filepath.Join(src, "config", "config.php"), []byte("<?php // fixture"))
	if !s.omitData {
		mkdirAll(t, filepath.Join(src, "data", "docs"))
		writeBytes(t, filepath.Join(src, "data", "docs", "a.txt"), []byte(strings.Repeat("payload\n", 200)))
	}
	passFile = filepath.Join(dir, "pass")
	writeBytes(t, passFile, []byte("testpass"))

	env := append(os.Environ(), "BORG_PASSPHRASE=testpass")
	runBorg(t, dir, env, "init", "--encryption=repokey", repo)
	args := []string{"create"}
	if s.archiveAge > 0 {
		args = append(args, "--timestamp", time.Now().UTC().Add(-s.archiveAge).Format("2006-01-02T15:04:05"))
	}
	args = append(args, repo+"::arch1", ".")
	runBorg(t, src, env, args...)
	return repo, passFile
}

func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func runBorg(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "borg", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("borg %v: %v\n%s", args, err, out)
	}
}
