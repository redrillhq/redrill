package store

import "time"

// ConfigHash is opaque to the store.
type Source struct {
	Name       string
	Type       string
	ConfigHash string
	CreatedAt  time.Time
}

// LevelsJSON is stored verbatim.
type Drill struct {
	Name        string
	Source      string
	ConfigHash  string
	MaxProofAge time.Duration
	LevelsJSON  string
}

type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
	TriggerAPI      Trigger = "api"
)

// Result is a finished run's verdict; a still-running run has the empty Result.
// fail (backup is bad) and error (couldn't check) are deliberately distinct.
type Result string

const (
	ResultPass  Result = "pass"
	ResultFail  Result = "fail"
	ResultError Result = "error"
)

// FinishedAt and Result stay zero/empty until FinishRun.
type Run struct {
	ID            int64
	Drill         string
	Trigger       Trigger
	StartedAt     time.Time
	FinishedAt    time.Time
	Result        Result
	LevelReached  string
	BytesRestored int64
	FilesRestored int64
	DurationMS    int64
	Executor      string
}

// Idx is assigned by the store on append.
type RunStep struct {
	RunID      int64
	Idx        int
	Kind       string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     string
	Summary    string
}

// Weak flags comfort-only checks (e.g. canary_file) so reports never treat
// them as proof.
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

// Artifact is a redacted log or report on disk.
type Artifact struct {
	RunID int64
	Idx   int
	Name  string
	Path  string
	Bytes int64
}

// DrillState is the last proven timestamp per (drill, level).
type DrillState struct {
	Drill        string
	Level        string
	LastProvenAt time.Time
}
