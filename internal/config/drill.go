package config

type Drill struct {
	Name        string   `yaml:"name"`
	Source      string   `yaml:"source"`
	Schedule    string   `yaml:"schedule"`
	Jitter      Duration `yaml:"jitter"`
	MaxProofAge Duration `yaml:"max_proof_age"`
	Timeout     Duration `yaml:"timeout"`
	Levels      Levels   `yaml:"levels"`
}

// At least one level must be configured.
type Levels struct {
	L1 *L1 `yaml:"l1"`
	L2 *L2 `yaml:"l2"`
	L3 *L3 `yaml:"l3"`
}

// L1 is integrity. Pointers distinguish unset from zero so a field belonging to
// the wrong source type is rejected.
type L1 struct {
	NativeCheck    *bool     `yaml:"native_check"`
	SnapshotMaxAge *Duration `yaml:"snapshot_max_age"`
	SizeAnomalyPct *int      `yaml:"size_anomaly_pct"`

	FileMinBytes    *Size     `yaml:"file_min_bytes"`
	CompressionTest *bool     `yaml:"compression_test"`
	MaxAge          *Duration `yaml:"max_age"`
}

// L2 is restorability: restore a sample (or full set) into scratch, then assert.
type L2 struct {
	Restore Restore `yaml:"restore"`
	Checks  []Check `yaml:"checks"`
}

type Restore struct {
	Scope        string   `yaml:"scope"` // sample | full
	Sample       *Sample  `yaml:"sample"`
	IncludePaths []string `yaml:"include_paths"`
}

// A sampled restore takes Files random files plus Newest newest.
type Sample struct {
	Files  int `yaml:"files"`
	Newest int `yaml:"newest"`
}

// L3 is usability: boot a sandbox from restored data and assert against it.
type L3 struct {
	ExtractPath string  `yaml:"extract_path"`
	Sandbox     Sandbox `yaml:"sandbox"`
	Load        string  `yaml:"load"` // auto | pg_restore | psql
	Checks      []Check `yaml:"checks"`
}

type Sandbox struct {
	Image   string            `yaml:"image"`
	Env     map[string]string `yaml:"env"`
	Network string            `yaml:"network"` // none (default; only mode in v1)
	Memory  Size              `yaml:"memory"`
	Timeout Duration          `yaml:"timeout"`
}

func (d *Drill) validate(path, srcType string, es *errset) {
	if d.Schedule == "" {
		es.add(path+".schedule", "required")
	}
	if d.Levels.L1 == nil && d.Levels.L2 == nil && d.Levels.L3 == nil {
		es.add(path+".levels", "at least one level (l1/l2/l3) required")
	}
	if d.Levels.L1 != nil {
		d.Levels.L1.validate(path+".levels.l1", srcType, es)
	}
	if d.Levels.L2 != nil {
		d.Levels.L2.validate(path+".levels.l2", es)
	}
	if d.Levels.L3 != nil {
		d.Levels.L3.validate(path+".levels.l3", srcType, es)
	}
}

func (l *L1) validate(path, srcType string, es *errset) {
	switch srcType {
	case "dumpdir":
		if l.NativeCheck != nil {
			es.add(path+".native_check", "not valid for dumpdir source")
		}
		if l.SnapshotMaxAge != nil {
			es.add(path+".snapshot_max_age", "not valid for dumpdir source")
		}
		if l.SizeAnomalyPct != nil {
			es.add(path+".size_anomaly_pct", "not valid for dumpdir source")
		}
	case "borg", "restic":
		if l.FileMinBytes != nil {
			es.add(path+".file_min_bytes", "not valid for %s source", srcType)
		}
		if l.CompressionTest != nil {
			es.add(path+".compression_test", "not valid for %s source", srcType)
		}
		if l.MaxAge != nil {
			es.add(path+".max_age", "not valid for %s source", srcType)
		}
	}
	if l.SizeAnomalyPct != nil && (*l.SizeAnomalyPct < 0 || *l.SizeAnomalyPct > 100) {
		es.add(path+".size_anomaly_pct", "must be 0..100, got %d", *l.SizeAnomalyPct)
	}
}

func (l *L2) validate(path string, es *errset) {
	if l.Restore.Scope != "" && l.Restore.Scope != "sample" && l.Restore.Scope != "full" {
		es.add(path+".restore.scope", "must be sample or full, got %q", l.Restore.Scope)
	}
	for i := range l.Checks {
		l.Checks[i].validate(checkPath(path, i), "l2", es)
	}
}

func (l *L3) validate(path, srcType string, es *errset) {
	if l.Sandbox.Image == "" {
		es.add(path+".sandbox.image", "required")
	}
	if l.Sandbox.Network != "" && l.Sandbox.Network != "none" {
		es.add(path+".sandbox.network", "only none is supported in v1, got %q", l.Sandbox.Network)
	}
	if l.Load != "" && l.Load != "auto" && l.Load != "pg_restore" && l.Load != "psql" {
		es.add(path+".load", "must be auto, pg_restore, or psql, got %q", l.Load)
	}
	// borg archives hold a tree, so L3 needs to know which dump to extract; a
	// dumpdir source is already a single file.
	if srcType == "borg" && l.ExtractPath == "" {
		es.add(path+".extract_path", "required for a borg L3 (the dump to extract from the archive)")
	}
	// Without a check an L3 could boot, load, and silently pass while proving
	// nothing.
	if len(l.Checks) == 0 {
		es.add(path+".checks", "at least one check is required (an L3 with no checks proves nothing)")
	}
	for i := range l.Checks {
		l.Checks[i].validate(checkPath(path, i), "l3", es)
	}
}

func checkPath(path string, i int) string {
	return path + ".checks[" + itoa(i) + "]"
}
