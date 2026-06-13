package store

import "time"

// Source mirrors the sources table (DESIGN §9.3). ConfigHash lets callers detect
// config drift; the store does not interpret it.
type Source struct {
	Name       string
	Type       string
	ConfigHash string
	CreatedAt  time.Time
}

// Drill mirrors the drills table. LevelsJSON is an opaque serialized blob the
// store stores verbatim; MaxProofAge feeds staleness, computed elsewhere.
type Drill struct {
	Name        string
	Source      string
	ConfigHash  string
	MaxProofAge time.Duration
	LevelsJSON  string
}

// Trigger is how a run was started.
type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
	TriggerAPI      Trigger = "api"
)

// Result is a finished run's verdict (DESIGN §9.8). A still-running run has the
// empty Result. fail (the backup is bad) and error (drillbit couldn't check)
// are deliberately distinct everywhere.
type Result string

const (
	ResultPass  Result = "pass"
	ResultFail  Result = "fail"
	ResultError Result = "error"
)

// Run mirrors the runs table. FinishedAt and Result stay zero/empty until the
// run finishes via FinishRun.
type Run struct {
	ID            int64
	Drill         string
	Trigger       Trigger
	StartedAt     time.Time
	FinishedAt    time.Time
	Result        Result
	LevelReached  string
	BytesRestored int64
	DurationMS    int64
	Executor      string
}

// RunStep mirrors the run_steps table. Idx is assigned by the store in insertion
// order (AddStep appends).
type RunStep struct {
	RunID      int64
	Idx        int
	Kind       string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     string
	Summary    string
}

// Evidence mirrors the evidence table: the expected/actual record for one check.
// Weak flags comfort-only checks (e.g. canary_file) so reports never let them
// masquerade as proof (DESIGN §4).
type Evidence struct {
	RunID     int64
	Idx       int
	CheckKind string
	Target    string
	Expected  string
	Actual    string
	Status    string
	Weak      bool
}

// Artifact mirrors the artifacts table: a redacted log or report on disk.
type Artifact struct {
	RunID int64
	Idx   int
	Name  string
	Path  string
	Bytes int64
}

// DrillState mirrors the drill_state table: the last proven timestamp for one
// (drill, level), advanced only on a full pass via RecordProof.
type DrillState struct {
	Drill        string
	Level        string
	LastProvenAt time.Time
}
