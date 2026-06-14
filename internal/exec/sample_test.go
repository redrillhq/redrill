package exec

import (
	"slices"
	"testing"
	"time"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/driver"
)

func sampleFiles() []driver.FileEntry {
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	return []driver.FileEntry{
		{Path: "config/config.php", Size: 10, Mtime: at(0), IsFile: true},
		{Path: "data/a.txt", Size: 100, Mtime: at(5), IsFile: true}, // newest
		{Path: "data/b.txt", Size: 200, Mtime: at(3), IsFile: true},
		{Path: "data/c.txt", Size: 300, Mtime: at(1), IsFile: true},
		{Path: "data/d.txt", Size: 400, Mtime: at(2), IsFile: true},
		{Path: "data", Size: 0, Mtime: at(0), IsFile: false}, // a directory — never selected
	}
}

func TestSelectSampleDeterministicAndShaped(t *testing.T) {
	t.Parallel()
	files := sampleFiles()
	sample := &config.Sample{Files: 2, Newest: 1}

	p1, total1 := selectSample(files, sample, []string{"config/"}, 42)
	p2, total2 := selectSample(files, sample, []string{"config/"}, 42)
	if !slices.Equal(p1, p2) || total1 != total2 {
		t.Fatalf("non-deterministic for the same seed: %v / %d vs %v / %d", p1, total1, p2, total2)
	}

	if !slices.Contains(p1, "config/config.php") {
		t.Error("include_path file not selected")
	}
	if !slices.Contains(p1, "data/a.txt") {
		t.Error("newest file not selected")
	}
	if slices.Contains(p1, "data") {
		t.Error("a directory was selected")
	}
	if !slices.IsSorted(p1) {
		t.Errorf("paths not sorted: %v", p1)
	}

	// total must equal the sum of the selected files' sizes.
	sz := map[string]int64{}
	for _, f := range files {
		sz[f.Path] = f.Size
	}
	var want int64
	for _, p := range p1 {
		want += sz[p]
	}
	if total1 != want {
		t.Errorf("total = %d, want %d", total1, want)
	}
}

func TestSelectSampleIncludeOnly(t *testing.T) {
	t.Parallel()
	// No sample config → only include_paths are restored.
	paths, total := selectSample(sampleFiles(), nil, []string{"config/"}, 1)
	if !slices.Equal(paths, []string{"config/config.php"}) || total != 10 {
		t.Errorf("got %v / %d, want [config/config.php] / 10", paths, total)
	}
}
