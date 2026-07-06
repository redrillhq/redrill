// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const kindFileCount = "file_count"

// FileCount asserts how many restored files match a glob (optionally at least
// MinSize bytes each) — the file-backup analog of an SQL assertion. Glob
// semantics follow gitignore intuition: a pattern without "/" matches file
// names at any depth; a pattern with "/" matches the restore-relative path,
// with "**" spanning any number of directories.
type FileCount struct {
	Glob    string
	MinSize int64
	Expect  string
}

func (c FileCount) Kind() string { return kindFileCount }

func (c FileCount) Run(_ context.Context, env CheckEnv) (Evidence, error) {
	ev := Evidence{Kind: kindFileCount, Target: c.Glob, Expected: c.Expect}
	exp, err := ParseExpect(c.Expect)
	if err != nil {
		ev.Status, ev.Actual = Error, "invalid expect: "+err.Error()
		return ev, nil
	}

	count := 0
	err = filepath.WalkDir(env.RestoreDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil // count real files; a dangling symlink is not restored content
		}
		rel, rerr := filepath.Rel(env.RestoreDir, p)
		if rerr != nil {
			return rerr
		}
		ok, merr := matchGlob(c.Glob, filepath.ToSlash(rel))
		if merr != nil {
			return merr
		}
		if !ok {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() < c.MinSize {
			return nil
		}
		count++
		return nil
	})
	if err != nil {
		ev.Status, ev.Actual = Error, "walk: "+err.Error()
		return ev, nil
	}

	actual := strconv.Itoa(count)
	ev.Actual = actual
	ok, err := exp.Evaluate(actual, env.Now)
	if err != nil {
		ev.Status, ev.Actual = Error, actual+": "+err.Error()
		return ev, nil
	}
	if ok {
		ev.Status = Pass
	} else {
		ev.Status = Fail
	}
	return ev, nil
}

// matchGlob matches a slash-separated relative path against pattern. Without
// a "/" the pattern applies to the base name at any depth; with one it
// applies to the whole relative path, "**" segments spanning zero or more
// directories.
func matchGlob(pattern, rel string) (bool, error) {
	if !strings.Contains(pattern, "/") {
		return path.Match(pattern, path.Base(rel))
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func matchSegments(pat, segs []string) (bool, error) {
	for {
		if len(pat) == 0 {
			return len(segs) == 0, nil
		}
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true, nil
			}
			for i := 0; i <= len(segs); i++ {
				ok, err := matchSegments(pat[1:], segs[i:])
				if ok || err != nil {
					return ok, err
				}
			}
			return false, nil
		}
		if len(segs) == 0 {
			return false, nil
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false, err
		}
		pat, segs = pat[1:], segs[1:]
	}
}
