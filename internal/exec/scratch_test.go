package exec

import (
	"math"
	"os"
	"testing"
)

func TestScratchPreflightQuota(t *testing.T) {
	t.Parallel()
	sc, err := newScratch(t.TempDir(), 1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.cleanup()

	if err := sc.preflight(500); err != nil {
		t.Errorf("under quota: %v, want nil", err)
	}
	if err := sc.preflight(2000); err == nil {
		t.Error("over quota: want error, not fail")
	}
}

func TestScratchPreflightFreeDisk(t *testing.T) {
	t.Parallel()
	sc, err := newScratch(t.TempDir(), 1, 0) // no quota
	if err != nil {
		t.Fatal(err)
	}
	defer sc.cleanup()
	if err := sc.preflight(math.MaxInt64); err == nil {
		t.Error("a restore larger than any disk should be refused")
	}
}

func TestScratchCleanup(t *testing.T) {
	t.Parallel()
	sc, err := newScratch(t.TempDir(), 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sc.root); err != nil {
		t.Fatalf("scratch not created: %v", err)
	}
	sc.cleanup()
	if _, err := os.Stat(sc.root); !os.IsNotExist(err) {
		t.Errorf("scratch not removed: %v", err)
	}
}
