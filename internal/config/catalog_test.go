// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"
)

// Every catalog row must be complete: a kind without a decoder would panic at
// parse time, and only unimplemented kinds may omit level entries.
func TestCatalogRowsComplete(t *testing.T) {
	t.Parallel()
	for kind, spec := range checkCatalog {
		if spec.decode == nil {
			t.Errorf("catalog row %q has no decode func (would panic in UnmarshalYAML)", kind)
		}
		if len(spec.levels) == 0 {
			t.Errorf("catalog row %q allows no level (unreachable kind)", kind)
		}
	}
}

// A misplaced check gets the placement error only — the level-scoped source
// rule must not add a second, misleading diagnostic (2026-07-04 review of
// commit 5476e9a).
func TestMisplacedHashMatchSingleError(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(`
version: 1
data_dir: /var/lib/redrill
scratch: {dir: /var/lib/redrill/scratch}
sources:
  - {name: dumps, type: dumpdir, path: /backups, pattern: "*.sql.gz"}
drills:
  - name: app-db
    source: dumps
    levels:
      l1: {file_min_bytes: 1}
      l3:
        sandbox: {image: postgres:16}
        checks:
          - sql: {query: "select 1", expect: "== 1"}
          - hash_match: true
`))
	if err == nil {
		t.Fatal("hash_match at L3 must not validate")
	}
	msg := err.Error()
	if !strings.Contains(msg, `check "hash_match" is not valid at L3`) {
		t.Errorf("missing the placement error:\n%s", msg)
	}
	if strings.Contains(msg, "dumpdir source") {
		t.Errorf("misplaced hash_match must get only the placement error, not the source rule too:\n%s", msg)
	}
}

// A mount-mode L2 with mount-compatible checks validates: the positive
// direction of the restore.mode rules.
func TestMountModeValidates(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(`
version: 1
data_dir: /var/lib/redrill
scratch: {dir: /var/lib/redrill/scratch}
sources:
  - {name: vault, type: borg, repo: "ssh://backup@nas/./repo", passphrase_file: /s/pass}
drills:
  - name: files
    source: vault
    max_proof_age: 10d
    levels:
      l2:
        restore: {mode: mount}
        checks:
          - path_exists: "config/config.php"
          - file_count: {glob: "**/*.jpg", min_size: 1, expect: "> 50"}
          - newest_file_max_age: 8d
`))
	if err != nil {
		t.Fatalf("mount-mode config must validate: %v", err)
	}
}

// At its correct level the source rule still fires.
func TestHashMatchDumpdirStillRejectedAtL2(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(`
version: 1
data_dir: /var/lib/redrill
scratch: {dir: /var/lib/redrill/scratch}
sources:
  - {name: dumps, type: dumpdir, path: /backups, pattern: "*.sql.gz"}
drills:
  - name: app-db
    source: dumps
    levels:
      l2:
        restore: {scope: full}
        checks:
          - hash_match: true
`))
	if err == nil || !strings.Contains(err.Error(), "hash_match is not valid for a dumpdir source") {
		t.Errorf("L2 dumpdir hash_match rejection lost: %v", err)
	}
}
