// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// In YAML each check is a single-key mapping whose key is the kind, e.g.
// {path_exists: "config/config.php"}.
type Check struct {
	Kind                  string
	Path                  string
	HashMatch             bool
	NewestFileMaxAge      Duration
	FileCountTolerancePct int
	MinTotalBytes         Size
	SQL                   *SQLCheck
	SQLNoError            string
	Exec                  string
}

type SQLCheck struct {
	Query  string `yaml:"query"`
	Expect string `yaml:"expect"`
}

const (
	checkPathExists       = "path_exists"
	checkPathAbsent       = "path_absent"
	checkHashMatch        = "hash_match"
	checkNewestFileMaxAge = "newest_file_max_age"
	checkFileCountTol     = "file_count_tolerance_pct"
	checkCanaryFile       = "canary_file"
	checkMinTotalBytes    = "min_total_bytes"
	checkSQL              = "sql"
	checkSQLNoError       = "sql_no_error"
	checkExec             = "exec"
)

// checkSpec is one row of the check-kind catalog: where the kind may appear
// and how its single-key payload decodes and validates. This table is the
// config half of the check-kind registry; the builder half lives in
// internal/exec, and a registry test there pins the two to each other, so a
// kind that validates but cannot be built (the M16-review false-pass class)
// fails CI instead of drifting.
type checkSpec struct {
	levels        map[string]bool
	unimplemented bool // in the DESIGN §7 catalog, not built yet
	decode        func(*Check, *yaml.Node) error
	validate      func(*Check, string, string, string, *errset) // (path, level, srcType)
}

var checkCatalog = map[string]checkSpec{
	checkPathExists: {levels: lvl("l2"), decode: decodePath, validate: needPath},
	checkPathAbsent: {levels: lvl("l2"), decode: decodePath, validate: needPath},
	checkCanaryFile: {levels: lvl("l2"), decode: decodePath, validate: needPath},
	checkHashMatch: {levels: lvl("l2"),
		decode: func(c *Check, n *yaml.Node) error { return n.Decode(&c.HashMatch) },
		validate: func(_ *Check, path, level, srcType string, es *errset) {
			// A dumpdir restore is a plain copy: nothing verifies restored bytes.
			// Level-scoped so a misplaced hash_match gets only the placement error.
			if level == "l2" && srcType == "dumpdir" {
				es.add(path, "hash_match is not valid for a dumpdir source (nothing verifies restored bytes)")
			}
		}},
	checkNewestFileMaxAge: {levels: lvl("l2"),
		decode: func(c *Check, n *yaml.Node) error { return n.Decode(&c.NewestFileMaxAge) }},
	checkFileCountTol: {levels: lvl("l2"),
		decode: func(c *Check, n *yaml.Node) error { return n.Decode(&c.FileCountTolerancePct) }},
	checkMinTotalBytes: {levels: lvl("l2"),
		decode: func(c *Check, n *yaml.Node) error { return n.Decode(&c.MinTotalBytes) }},
	checkSQL: {levels: lvl("l3"),
		decode: func(c *Check, n *yaml.Node) error {
			var q SQLCheck
			err := n.Decode(&q)
			if err == nil {
				err = knownKeys(n, "query", "expect")
			}
			c.SQL = &q
			return err
		},
		validate: func(c *Check, path, _, _ string, es *errset) {
			if c.SQL == nil || c.SQL.Query == "" {
				es.add(path, "sql requires a query")
			}
			if c.SQL != nil && c.SQL.Expect == "" {
				es.add(path, "sql requires an expect predicate")
			}
		}},
	checkSQLNoError: {levels: lvl("l3"),
		decode: func(c *Check, n *yaml.Node) error { return n.Decode(&c.SQLNoError) },
		validate: func(c *Check, path, _, _ string, es *errset) {
			if c.SQLNoError == "" {
				es.add(path, "sql_no_error requires a query")
			}
		}},
	// exec is the escape hatch: an operator-authored command, run in the
	// restored tree at L2 or inside the sandbox at L3; exit code = verdict.
	checkExec: {levels: lvl("l2", "l3"),
		decode: func(c *Check, n *yaml.Node) error { return n.Decode(&c.Exec) },
		validate: func(c *Check, path, _, _ string, es *errset) {
			if c.Exec == "" {
				es.add(path, "exec requires a command")
			}
		}},
}

func lvl(levels ...string) map[string]bool {
	m := make(map[string]bool, len(levels))
	for _, l := range levels {
		m[l] = true
	}
	return m
}

func decodePath(c *Check, n *yaml.Node) error { return n.Decode(&c.Path) }

func needPath(c *Check, path, _, _ string, es *errset) {
	if c.Path == "" {
		es.add(path, "%s requires a path", c.Kind)
	}
}

// CheckKinds returns the implemented check kinds valid at level, sorted. The
// exec builder registries are pinned to this in a cross-package test.
func CheckKinds(level string) []string {
	var out []string
	for kind, spec := range checkCatalog {
		if !spec.unimplemented && spec.levels[level] {
			out = append(out, kind)
		}
	}
	sort.Strings(out)
	return out
}

func (c *Check) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return fmt.Errorf(`each check must be a single-key mapping like {path_exists: "data/"}`)
	}
	key := n.Content[0].Value
	spec, ok := checkCatalog[key]
	if !ok {
		return fmt.Errorf("unknown check kind %q", key)
	}
	c.Kind = key
	if err := spec.decode(c, n.Content[1]); err != nil {
		return fmt.Errorf("check %q: %w", key, err)
	}
	return nil
}

func (c *Check) validate(path, level, srcType string, es *errset) {
	spec := checkCatalog[c.Kind] // decode guarantees the kind is cataloged
	if spec.unimplemented {
		es.add(path, "the %s check is not implemented yet", c.Kind)
		return
	}
	if !spec.levels[level] {
		es.add(path, "check %q is not valid at %s", c.Kind, strings.ToUpper(level))
	}
	if spec.validate != nil {
		spec.validate(c, path, level, srcType, es)
	}
}

// knownKeys rejects any key outside the allowed set; a custom Unmarshaler
// bypasses the decoder's KnownFields setting.
func knownKeys(n *yaml.Node, allowed ...string) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping")
	}
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i].Value
		found := false
		for _, a := range allowed {
			if a == k {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown key %q", k)
		}
	}
	return nil
}
