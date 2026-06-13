package checks

import "context"

// Status is a single check's verdict (DESIGN §9.8). A check that runs yields
// exactly one of these; skipped is a level/run state, not a check result, and is
// owned by the orchestrator.
type Status string

const (
	Pass  Status = "pass"  // predicate held
	Fail  Status = "fail"  // predicate false — the backup is the problem
	Error Status = "error" // couldn't evaluate — the auditor is the problem
)

// Evidence is the expected/actual record a check produces (DESIGN §9.2). The
// orchestrator persists it (as a store row); Weak flags comfort-only checks
// (e.g. canary_file) so reports never let them pass as proof (DESIGN §4).
type Evidence struct {
	Target   string // what was checked: a path, query, …
	Expected string // the predicate, human-readable
	Actual   string // what was observed
	Status   Status
	Weak     bool
}

// CheckEnv carries what a check needs to run, supplied by the orchestrator.
// Fields grow per milestone: RestoreDir (L2 restores, M5/M7) is here now; a
// sandbox handle (L3, M8) and prior-run baselines (tolerance checks, M7) arrive
// with those milestones. Checks never reach back into the store or global state
// (ARCHITECTURE import rules) — everything they use is passed in here.
type CheckEnv struct {
	RestoreDir string
}

// Check is one typed assertion producing Evidence (DESIGN §9.2; signature is
// normative). Run reports a false predicate as Evidence{Status: Fail} and an
// unevaluable check as Evidence{Status: Error}; it returns a non-nil error only
// when it cannot produce Evidence at all (which the orchestrator also treats as
// Error). fail and error are never conflated.
type Check interface {
	Kind() string
	Run(ctx context.Context, env CheckEnv) (Evidence, error)
}
