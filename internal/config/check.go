// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"

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

var l2Checks = map[string]bool{
	checkPathExists: true, checkPathAbsent: true, checkHashMatch: true,
	checkNewestFileMaxAge: true, checkFileCountTol: true, checkCanaryFile: true,
	checkMinTotalBytes: true,
}

var l3Checks = map[string]bool{checkSQL: true, checkSQLNoError: true}

func (c *Check) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return fmt.Errorf(`each check must be a single-key mapping like {path_exists: "data/"}`)
	}
	key := n.Content[0].Value
	val := n.Content[1]
	c.Kind = key
	var err error
	switch key {
	case checkPathExists, checkPathAbsent, checkCanaryFile:
		err = val.Decode(&c.Path)
	case checkHashMatch:
		err = val.Decode(&c.HashMatch)
	case checkNewestFileMaxAge:
		err = val.Decode(&c.NewestFileMaxAge)
	case checkFileCountTol:
		err = val.Decode(&c.FileCountTolerancePct)
	case checkMinTotalBytes:
		err = val.Decode(&c.MinTotalBytes)
	case checkSQL:
		var q SQLCheck
		if err = val.Decode(&q); err == nil {
			err = knownKeys(val, "query", "expect")
		}
		c.SQL = &q
	case checkSQLNoError:
		err = val.Decode(&c.SQLNoError)
	case checkExec:
		err = val.Decode(&c.Exec)
	default:
		return fmt.Errorf("unknown check kind %q", key)
	}
	if err != nil {
		return fmt.Errorf("check %q: %w", key, err)
	}
	return nil
}

func (c *Check) validate(path, level string, es *errset) {
	switch level {
	case "l2":
		if !l2Checks[c.Kind] && c.Kind != checkExec {
			es.add(path, "check %q is not valid at L2", c.Kind)
		}
	case "l3":
		if !l3Checks[c.Kind] && c.Kind != checkExec {
			es.add(path, "check %q is not valid at L3", c.Kind)
		}
	}
	switch c.Kind {
	case checkPathExists, checkPathAbsent, checkCanaryFile:
		if c.Path == "" {
			es.add(path, "%s requires a path", c.Kind)
		}
	case checkSQL:
		if c.SQL == nil || c.SQL.Query == "" {
			es.add(path, "sql requires a query")
		}
		if c.SQL != nil && c.SQL.Expect == "" {
			es.add(path, "sql requires an expect predicate")
		}
	case checkSQLNoError:
		if c.SQLNoError == "" {
			es.add(path, "sql_no_error requires a query")
		}
	case checkExec:
		if c.Exec == "" {
			es.add(path, "exec requires a command")
		}
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
