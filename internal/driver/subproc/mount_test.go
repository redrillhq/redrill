// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package subproc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fake "mount": touches a marker in dir (the readiness signal) and stays
// alive until dir/stop appears (the "unmount" makes it exit, like a FUSE
// daemon after fusermount -u).
func fakeMountArgs(dir string) []string {
	script := `mkdir -p "$1" && touch "$1/served" && while [ ! -f "$1/stop" ]; do sleep 0.05; done`
	return []string{"-c", script, "fake-mount", dir}
}

// The lifecycle tests stay sequential: TestStartMountNeverServes shortens the
// package's timeout vars, and a parallel reader would race it.
func TestStartMountServesAndStops(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mnt")
	unmounted := false
	m, err := StartMount(context.Background(), "", nil, "sh", fakeMountArgs(dir),
		func() bool { return DirServing(dir) },
		func() error {
			unmounted = true
			return os.WriteFile(filepath.Join(dir, "stop"), nil, 0o600)
		})
	if err != nil {
		t.Fatalf("StartMount: %v", err)
	}
	if !DirServing(dir) {
		t.Fatal("mountpoint not serving after StartMount returned")
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !unmounted {
		t.Fatal("Stop did not run the unmount step")
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("second Stop must be idempotent: %v", err)
	}
}

// A mount process that dies before serving is an error naming the exit, not
// a hang or a false ready.
func TestStartMountChildExitsEarly(t *testing.T) {
	dir := t.TempDir() // never gets a marker
	_, err := StartMount(context.Background(), "", nil, "sh", []string{"-c", "exit 3"},
		func() bool { return DirServing(filepath.Join(dir, "nope")) }, nil)
	if err == nil || !strings.Contains(err.Error(), "exited before serving") {
		t.Fatalf("err = %v, want exited-before-serving", err)
	}
}

// A mount that never serves trips the readiness timeout and is reaped.
func TestStartMountNeverServes(t *testing.T) {
	old := mountReadyTimeout
	oldGrace := mountStopGrace
	mountReadyTimeout, mountStopGrace = 600*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { mountReadyTimeout, mountStopGrace = old, oldGrace })

	_, err := StartMount(context.Background(), "", nil, "sleep", []string{"60"},
		func() bool { return false }, nil)
	if err == nil || !strings.Contains(err.Error(), "not serving after") {
		t.Fatalf("err = %v, want readiness timeout", err)
	}
}

func TestDirServing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if DirServing(dir) {
		t.Error("empty dir must not read as serving")
	}
	if DirServing(filepath.Join(dir, "missing")) {
		t.Error("missing dir must not read as serving")
	}
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !DirServing(dir) {
		t.Error("dir with an entry must read as serving")
	}
}
