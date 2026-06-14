package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadValidFixtures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"testdata/appendix_a.yaml", "testdata/full_example.yaml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(name); err != nil {
				t.Fatalf("Load(%s) = %v, want valid", name, err)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := Load("testdata/does-not-exist.yaml"); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestAppendixAShape(t *testing.T) {
	t.Parallel()
	c, err := Load("testdata/appendix_a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.Sources); got != 2 {
		t.Fatalf("sources = %d, want 2", got)
	}
	if c.Sources[1].Pick != "newest" {
		t.Errorf("pg-dumps pick = %q, want newest", c.Sources[1].Pick)
	}
	files := c.Drills[0].Levels.L2
	if files == nil || files.Restore.Sample == nil || files.Restore.Sample.Files != 200 {
		t.Errorf("nextcloud-files L2 sample.files mismatch: %+v", files)
	}
	if got := c.Drills[0].Levels.L1.SnapshotMaxAge.Duration(); got != 36*time.Hour {
		t.Errorf("snapshot_max_age = %v, want 36h", got)
	}
	db := c.Drills[2].Levels.L3
	if db == nil || db.Load != "auto" || db.Sandbox.Network != "none" {
		t.Errorf("app-db L3 defaults mismatch: %+v", db)
	}
	if got := db.Sandbox.Memory.Bytes(); got != 1<<30 {
		t.Errorf("sandbox memory = %d, want %d", got, int64(1)<<30)
	}
	if db.Checks[0].Kind != "sql" || db.Checks[0].SQL.Expect != "> 0" {
		t.Errorf("app-db first check mismatch: %+v", db.Checks[0])
	}
}

func TestDefaultsApplied(t *testing.T) {
	t.Parallel()
	c, err := Parse([]byte(`
version: 1
data_dir: /v
scratch: {dir: /s}
sources: [{name: s, type: dumpdir, path: /p, pattern: "*.gz"}]
drills:
  - name: d
    source: s
    schedule: "x"
    levels:
      l3:
        sandbox: {image: postgres:16}
        checks: [{sql_no_error: "select 1"}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Concurrency != 1 {
		t.Errorf("concurrency default = %d, want 1", c.Concurrency)
	}
	if c.Sources[0].Pick != "newest" {
		t.Errorf("pick default = %q, want newest", c.Sources[0].Pick)
	}
	l3 := c.Drills[0].Levels.L3
	if l3.Load != "auto" || l3.Sandbox.Network != "none" {
		t.Errorf("L3 defaults: load=%q network=%q, want auto/none", l3.Load, l3.Sandbox.Network)
	}
}

const validBase = `
version: 1
data_dir: /v
scratch: {dir: /s}
sources: [{name: s, type: borg, repo: "r"}]
drills: [{name: d, source: s, schedule: "x", levels: {l1: {native_check: true}}}]
`

func TestParseValidBase(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte(validBase)); err != nil {
		t.Fatalf("validBase should parse: %v", err)
	}
}

func TestRetentionParses(t *testing.T) {
	t.Parallel()
	c, err := Parse([]byte("version: 1\ndata_dir: /v\nscratch: {dir: /s}\n" +
		"sources: [{name: s, type: borg, repo: r}]\n" +
		"drills: [{name: d, source: s, schedule: x, retention: {max_age: 90d, max_count: 50}, levels: {l1: {native_check: true}}}]\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	r := c.Drills[0].Retention
	if r.MaxAge.Duration() != 90*24*time.Hour || r.MaxCount != 50 {
		t.Errorf("retention = %+v, want 90d/50", r)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		want string // substring the error must contain
	}{
		// structural: strict parsing
		{"unknown top key", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nbogus: 1\n", "bogus"},
		{"unknown scratch key", "version: 1\ndata_dir: /v\nscratch: {dir: /s, nope: 1}\n", "nope"},
		{"unknown source key", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r, nope: 1}]\n", "nope"},
		{"unknown drill key", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, nope: 1, levels: {l1: {native_check: true}}}]\n", "nope"},
		{"unknown level key", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l1: {nope: 1}}}]\n", "nope"},
		{"inline secret rejected", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r, passphrase: hunter2}]\n", "passphrase"},
		{"bad duration", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, jitter: 5wat, levels: {l1: {native_check: true}}}]\n", "duration"},
		{"bad size", "version: 1\ndata_dir: /v\nscratch: {dir: /s, max_bytes: 5Gigs}\n", "size"},
		{"wrong scalar type", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nconcurrency: abc\n", "cannot unmarshal"},
		{"multi-key check", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l2: {checks: [{path_exists: a, hash_match: true}]}}}]\n", "single-key"},
		{"unknown check kind", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l2: {checks: [{bogus: 1}]}}}]\n", "unknown check kind"},
		{"check not a mapping", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l2: {checks: [\"foo\"]}}}]\n", "single-key"},
		{"nested sql unknown key", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p}, checks: [{sql: {query: q, expect: e, foo: 1}}]}}}]\n", `unknown key "foo"`},

		// semantic: validation
		{"version missing", "data_dir: /v\nscratch: {dir: /s}\n", "version"},
		{"version wrong", "version: 2\ndata_dir: /v\nscratch: {dir: /s}\n", "version"},
		{"data_dir missing", "version: 1\nscratch: {dir: /s}\n", "data_dir"},
		{"scratch dir missing", "version: 1\ndata_dir: /v\n", "scratch.dir"},
		{"concurrency negative", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nconcurrency: -1\n", "concurrency"},
		{"nice io_class bad", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nnice: {io_class: turbo}\n", "io_class"},
		{"notify event bad", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nnotify: {events: [boom]}\n", "notify.events[0]"},
		{"server listen bad", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nserver: {listen: noport}\n", "server.listen"},
		{"healthchecks url bad", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nnotify: {healthchecks_url: \"not a url\"}\n", "healthchecks_url"},
		{"source name missing", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{type: borg, repo: r}]\n", "sources[0].name"},
		{"source dup name", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}, {name: s, type: borg, repo: r2}]\n", "duplicate source"},
		{"source type missing", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, repo: r}]\n", "sources[0].type"},
		{"source type unknown", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: tarball, repo: r}]\n", "unknown source type"},
		{"borg missing repo", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg}]\n", "sources[0].repo"},
		{"dumpdir missing path", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: dumpdir, pattern: \"*.gz\"}]\n", "sources[0].path"},
		{"dumpdir missing pattern", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: dumpdir, path: /p}]\n", "sources[0].pattern"},
		{"dumpdir bad pick", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: dumpdir, path: /p, pattern: \"*.gz\", pick: random}]\n", "pick"},
		{"restic missing password", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: restic, repo: r}]\n", "password_file or password_env"},
		{"cross-type field", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: dumpdir, path: /p, pattern: \"*.gz\", repo: r}]\n", "not valid for dumpdir"},
		{"drill name missing", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{source: s, schedule: x, levels: {l1: {native_check: true}}}]\n", "drills[0].name"},
		{"drill dup name", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l1: {native_check: true}}}, {name: d, source: s, schedule: y, levels: {l1: {native_check: true}}}]\n", "duplicate drill"},
		{"drill source missing", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, schedule: x, levels: {l1: {native_check: true}}}]\n", "drills[0].source"},
		{"drill source unknown", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: nope, schedule: x, levels: {l1: {native_check: true}}}]\n", "no such source"},
		{"drill no levels", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {}}]\n", "at least one level"},
		{"drill schedule missing", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, levels: {l1: {native_check: true}}}]\n", "schedule"},
		{"retention negative count", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, retention: {max_count: -1}, levels: {l1: {native_check: true}}}]\n", "retention.max_count"},
		{"retention negative age", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, retention: {max_age: -5h}, levels: {l1: {native_check: true}}}]\n", "negative duration"},
		{"retention unknown key", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, retention: {nope: 1}, levels: {l1: {native_check: true}}}]\n", "nope"},
		{"l1 dumpdir field on borg", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l1: {file_min_bytes: 1MiB}}}]\n", "levels.l1.file_min_bytes"},
		{"l1 borg field on dumpdir", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: dumpdir, path: /p, pattern: \"*.gz\"}]\ndrills: [{name: d, source: s, schedule: x, levels: {l1: {snapshot_max_age: 36h}}}]\n", "levels.l1.snapshot_max_age"},
		{"l1 size anomaly range", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l1: {size_anomaly_pct: 150}}}]\n", "0..100"},
		{"l2 bad scope", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l2: {restore: {scope: half}}}}]\n", "restore.scope"},
		{"l2 check wrong level", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l2: {checks: [{sql: {query: q, expect: e}}]}}}]\n", "not valid at L2"},
		{"l3 missing image", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {}, checks: [{sql_no_error: q}]}}}]\n", "sandbox.image"},
		{"l3 bad network", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p, network: host}, checks: [{sql_no_error: q}]}}}]\n", "only none is supported"},
		{"l3 bad load", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p}, load: mysqldump, checks: [{sql_no_error: q}]}}}]\n", "load"},
		{"l3 check wrong level", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p}, checks: [{path_exists: a}]}}}]\n", "not valid at L3"},
		{"sql missing query", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p}, checks: [{sql: {expect: \"> 0\"}}]}}}]\n", "sql requires a query"},
		{"sql missing expect", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p}, checks: [{sql: {query: q}}]}}}]\n", "expect predicate"},
		{"path_exists empty", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l2: {checks: [{path_exists: \"\"}]}}}]\n", "path_exists requires a path"},
		{"l3 no checks", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: dumpdir, path: /p, pattern: \"*.gz\"}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p}}}}]\n", "at least one check"},
		{"borg l3 missing extract_path", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: borg, repo: r}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p}, checks: [{sql_no_error: q}]}}}]\n", "extract_path"},
		{"restic l3 missing extract_path", "version: 1\ndata_dir: /v\nscratch: {dir: /s}\nsources: [{name: s, type: restic, repo: r, password_env: PW}]\ndrills: [{name: d, source: s, schedule: x, levels: {l3: {sandbox: {image: p}, checks: [{sql_no_error: q}]}}}]\n", "extract_path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("Parse() = nil error, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}
