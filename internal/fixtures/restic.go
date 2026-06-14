package fixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RequireRestic skips the test unless the restic binary is on PATH.
func RequireRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed; provide restic to run engine fixtures")
	}
}

type resticSpec struct {
	snapshotAge time.Duration
	omitData    bool
	files       []resticFile
}

type resticFile struct{ rel, content string }

type ResticOption func(*resticSpec)

// ResticFile places an extra file at rel inside the snapshot — e.g. a Postgres
// dump for a restic L3 drill's extract_path.
func ResticFile(rel, content string) ResticOption {
	return func(s *resticSpec) { s.files = append(s.files, resticFile{rel, content}) }
}

// ResticSnapshotAge backdates the snapshot by d via restic backup --time (a stale
// source).
func ResticSnapshotAge(d time.Duration) ResticOption {
	return func(s *resticSpec) { s.snapshotAge = d }
}

// ResticOmitData drops the data/ tree from the snapshot (a bad exclude /
// missing-data-dir).
func ResticOmitData() ResticOption {
	return func(s *resticSpec) { s.omitData = true }
}

// Restic builds a restic repo with a config/ tree (and, unless ResticOmitData, a
// data/ tree) and one snapshot, returning the repo path and the password file.
// The single backed-up root is stripped on restore, so checks use relative paths.
// Requires restic on PATH — gate with RequireRestic.
func Restic(t *testing.T, opts ...ResticOption) (repo, passFile string) {
	t.Helper()
	var s resticSpec
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
	for _, f := range s.files {
		p := filepath.Join(src, filepath.FromSlash(f.rel))
		mkdirAll(t, filepath.Dir(p))
		writeBytes(t, p, []byte(f.content))
	}
	passFile = filepath.Join(dir, "pass")
	writeBytes(t, passFile, []byte("testpass"))

	env := append(os.Environ(), "RESTIC_PASSWORD=testpass", "RESTIC_REPOSITORY="+repo)
	runRestic(t, dir, env, "init")
	args := []string{"backup"}
	if s.snapshotAge > 0 {
		args = append(args, "--time", time.Now().UTC().Add(-s.snapshotAge).Format("2006-01-02 15:04:05"))
	}
	args = append(args, src)
	runRestic(t, dir, env, args...)
	return repo, passFile
}

func runRestic(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "restic", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restic %v: %v\n%s", args, err, out)
	}
}
