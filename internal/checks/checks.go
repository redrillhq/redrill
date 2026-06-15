// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package checks

import (
	"context"
	"time"

	"github.com/alyamovsky/redrill/internal/sandbox"
)

// skipped is a run state, not a check result.
type Status string

const (
	Pass  Status = "pass"  // predicate held
	Fail  Status = "fail"  // predicate false — the backup is the problem
	Error Status = "error" // couldn't evaluate — the auditor is the problem
)

type Evidence struct {
	Kind     string
	Target   string
	Expected string
	Actual   string
	Status   Status
	Weak     bool // comfort-only check; never counts as proof
}

type CheckEnv struct {
	RestoreDir string
	Now        time.Time // injected clock for age checks
	Sandbox    sandbox.Sandbox
}

// Run returns a non-nil error only when it cannot produce Evidence at all.
type Check interface {
	Kind() string
	Run(ctx context.Context, env CheckEnv) (Evidence, error)
}
