// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package exec

import (
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/redrillhq/redrill/internal/config"
	"github.com/redrillhq/redrill/internal/driver"
)

// selectSample picks paths to restore for L2: every file under an include_path,
// the M newest by mtime, and N others chosen by a seed (the run id) that keeps
// selection reproducible yet varies coverage across runs.
func selectSample(files []driver.FileEntry, sample *config.Sample, includePaths []string, seed uint64) ([]string, int64) {
	picked := map[string]int64{}
	var pool []driver.FileEntry
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

func underAny(path string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
