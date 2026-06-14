package exec

import (
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/alyamovsky/redrill/internal/config"
	"github.com/alyamovsky/redrill/internal/driver"
)

// selectSample picks which paths of an archive to restore for L2: every file
// under an include_path, plus the M newest by mtime, plus N seeded-random
// others (DESIGN §6). The seed makes selection reproducible (TESTING.md); the
// executor seeds it from the run id so coverage varies across runs. Returns the
// chosen paths (sorted) and their total size, for the scratch preflight.
func selectSample(files []driver.FileEntry, sample *config.Sample, includePaths []string, seed uint64) ([]string, int64) {
	picked := map[string]int64{}
	var pool []driver.FileEntry // regular files not already pinned by include_paths
	for _, f := range files {
		if !f.IsFile {
			continue
		}
		if underAny(f.Path, includePaths) {
			picked[f.Path] = f.Size
		} else {
			pool = append(pool, f)
		}
	}

	if sample != nil {
		newest := append([]driver.FileEntry(nil), pool...)
		sort.Slice(newest, func(i, j int) bool {
			if newest[i].Mtime.Equal(newest[j].Mtime) {
				return newest[i].Path < newest[j].Path
			}
			return newest[i].Mtime.After(newest[j].Mtime)
		})
		for i := 0; i < sample.Newest && i < len(newest); i++ {
			picked[newest[i].Path] = newest[i].Size
		}

		//nolint:gosec // G404: deterministic, seeded sampling — not security-sensitive
		r := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
		perm := r.Perm(len(pool))
		for i := 0; i < sample.Files && i < len(perm); i++ {
			f := pool[perm[i]]
			picked[f.Path] = f.Size
		}
	}

	paths := make([]string, 0, len(picked))
	var total int64
	for p, sz := range picked {
		paths = append(paths, p)
		total += sz
	}
	sort.Strings(paths)
	return paths, total
}

// underAny reports whether path is one of, or sits under, any of the prefixes.
func underAny(path string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
