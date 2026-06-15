// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import "testing"

// Parse must never panic on arbitrary bytes, must not return a config alongside
// an error, and any config it accepts must satisfy its own validation
// idempotently (no config slips through in an invalid state).
func FuzzParse(f *testing.F) {
	seeds := []string{
		"version: 1\ndata_dir: /var/lib/redrill\nscratch:\n  dir: /var/lib/redrill/scratch\n",
		"version: 2\n",
		"",
		"not: valid: yaml: : :",
		"version: 1\nunknown_top_level_key: x\n",
		"version: 1\ndata_dir: /v\nscratch: {dir: /s}\n" +
			"sources:\n  - {name: a, type: dumpdir, path: /p, pattern: \"*.sql.gz\"}\n" +
			"drills:\n  - {name: d, source: a, schedule: \"@daily\", levels: {l1: {file_min_bytes: 1MiB, compression_test: true}}}\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := Parse(data)
		if err != nil {
			if c != nil {
				t.Fatalf("Parse returned both a config and an error")
			}
			return
		}
		if c == nil {
			t.Fatal("Parse returned a nil config with a nil error")
		}
		if verr := c.Validate(); verr != nil {
			t.Fatalf("Parse accepted a config that fails Validate: %v", verr)
		}
	})
}
