// Copyright (C) 2026 Andrew Alyamovsky
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// scanner is the common surface of *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

const runColumns = `id, drill, "trigger", started_at, finished_at, result, level_reached, bytes_restored, files_restored, duration_ms, executor`

const (
	runByID       = `SELECT ` + runColumns + ` FROM runs WHERE id = ?`
	runsByDrill   = `SELECT ` + runColumns + ` FROM runs WHERE drill = ? ORDER BY id DESC`
	runsByDrillN  = `SELECT ` + runColumns + ` FROM runs WHERE drill = ? ORDER BY id DESC LIMIT ?`
	nextStepIdx   = `(SELECT COALESCE(MAX(idx) + 1, 0) FROM run_steps WHERE run_id = ?)`
	nextEvidIdx   = `(SELECT COALESCE(MAX(idx) + 1, 0) FROM evidence WHERE run_id = ?)`
	nextArtifIdx  = `(SELECT COALESCE(MAX(idx) + 1, 0) FROM artifacts WHERE run_id = ?)`
	stepsByRun    = `SELECT run_id, idx, kind, started_at, finished_at, status, summary FROM run_steps WHERE run_id = ? ORDER BY idx`
	evidenceByRun = `SELECT run_id, idx, check_kind, target, expected, actual, status, weak FROM evidence WHERE run_id = ? ORDER BY idx`
	artifactByRun = `SELECT run_id, idx, name, path, bytes FROM artifacts WHERE run_id = ? ORDER BY idx`
)

// CreateRun inserts an unfinished run: identity fields only, the outcome
// arrives with FinishRun.
func (s *Store) CreateRun(ctx context.Context, r Run) (int64, error) {
	switch {
	case r.Drill == "":
		return 0, fmt.Errorf("create run: drill required")
	case r.Trigger == "":
		return 0, fmt.Errorf("create run for %s: trigger required", r.Drill)
	case r.StartedAt.IsZero():
		return 0, fmt.Errorf("create run for %s: started_at required", r.Drill)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO runs ("trigger", drill, started_at, executor)
		VALUES (?, ?, ?, ?)`,
		string(r.Trigger), r.Drill, unixNano(r.StartedAt), r.Executor)
	if err != nil {
		return 0, fmt.Errorf("create run for %s: %w", r.Drill, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create run for %s: %w", r.Drill, err)
	}
	return id, nil
}

// FinishRun records a run's outcome by r.ID, exactly once — a recorded verdict
// is immutable. Returns wrapped ErrNotFound for an unknown id.
func (s *Store) FinishRun(ctx context.Context, r Run) error {
	if r.ID == 0 {
		return fmt.Errorf("finish run: id required")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE runs SET finished_at = ?, result = ?, level_reached = ?, bytes_restored = ?, files_restored = ?, duration_ms = ?
		WHERE id = ? AND result IS NULL`,
		nullTime(r.FinishedAt), nullResult(r.Result), r.LevelReached, r.BytesRestored, r.FilesRestored, r.DurationMS, r.ID)
	if err != nil {
		return fmt.Errorf("finish run %d: %w", r.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish run %d: %w", r.ID, err)
	}
	if n == 0 {
		var one int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE id = ?`, r.ID).Scan(&one); err == nil {
			return fmt.Errorf("finish run %d: already finished", r.ID)
		}
		return fmt.Errorf("finish run %d: %w", r.ID, ErrNotFound)
	}
	return nil
}

// GetRun returns wrapped ErrNotFound when absent.
func (s *Store) GetRun(ctx context.Context, id int64) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, runByID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("get run %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("get run %d: %w", id, err)
	}
	return r, nil
}

// ListRuns returns a drill's runs, newest first. A limit <= 0 returns all.
func (s *Store) ListRuns(ctx context.Context, drill string, limit int) ([]Run, error) {
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = s.db.QueryContext(ctx, runsByDrillN, drill, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, runsByDrill, drill)
	}
	if err != nil {
		return nil, fmt.Errorf("list runs for %s: %w", drill, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list runs for %s: %w", drill, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs for %s: %w", drill, err)
	}
	return out, nil
}

// LastBaselineRun returns the most recent passing run that actually restored
// files (the file_count_tolerance baseline), ok=false if none. Restore-less
// passes (e.g. an L1-only run) must not reset the baseline to zero.
func (s *Store) LastBaselineRun(ctx context.Context, drill string) (Run, bool, error) {
	q := `SELECT ` + runColumns + ` FROM runs WHERE drill = ? AND result = 'pass' AND files_restored > 0 ORDER BY id DESC LIMIT 1`
	r, err := scanRun(s.db.QueryRowContext(ctx, q, drill))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("baseline run for %s: %w", drill, err)
	}
	return r, true, nil
}

// MarkAbandonedRuns finalizes runs a crashed process left without a verdict as
// error, returning how many. Called at daemon startup, like the sandbox janitor.
func (s *Store) MarkAbandonedRuns(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET finished_at = ?, result = 'error' WHERE result IS NULL`, unixNano(now))
	if err != nil {
		return 0, fmt.Errorf("mark abandoned runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark abandoned runs: %w", err)
	}
	return n, nil
}

// LatestFinishedRun returns a drill's most recent run with a recorded verdict
// (any of pass/fail/error), ok=false if none. An unfinished run is skipped.
func (s *Store) LatestFinishedRun(ctx context.Context, drill string) (Run, bool, error) {
	q := `SELECT ` + runColumns + ` FROM runs WHERE drill = ? AND result IS NOT NULL ORDER BY id DESC LIMIT 1`
	r, err := scanRun(s.db.QueryRowContext(ctx, q, drill))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("latest finished run for %s: %w", drill, err)
	}
	return r, true, nil
}

// AddBytesRestored accumulates a drill's monotonic restored-bytes counter. It
// lives in drill_counters, which retention never prunes, so the Prometheus
// counter can't regress when old runs are deleted.
func (s *Store) AddBytesRestored(ctx context.Context, drill string, n int64) error {
	if n <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drill_counters (drill, bytes_restored_total)
		VALUES (?, ?)
		ON CONFLICT(drill) DO UPDATE SET bytes_restored_total = bytes_restored_total + excluded.bytes_restored_total`,
		drill, n)
	if err != nil {
		return fmt.Errorf("add bytes restored for %s: %w", drill, err)
	}
	return nil
}

// BytesRestoredTotal returns a drill's monotonic restored-bytes counter; 0 if none.
func (s *Store) BytesRestoredTotal(ctx context.Context, drill string) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT bytes_restored_total FROM drill_counters WHERE drill = ?`, drill).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("bytes restored total for %s: %w", drill, err)
	}
	return n.Int64, nil
}

func scanRun(sc scanner) (Run, error) {
	var (
		r        Run
		trigger  string
		started  int64
		finished sql.NullInt64
		result   sql.NullString
	)
	if err := sc.Scan(&r.ID, &r.Drill, &trigger, &started, &finished, &result,
		&r.LevelReached, &r.BytesRestored, &r.FilesRestored, &r.DurationMS, &r.Executor); err != nil {
		return Run{}, err
	}
	r.Trigger = Trigger(trigger)
	r.StartedAt = timeFromUnixNano(started)
	r.FinishedAt = timeFromNull(finished)
	r.Result = Result(result.String)
	return r, nil
}

func nullResult(r Result) sql.NullString {
	if r == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: string(r), Valid: true}
}

// AddStep appends a step to a run. A run is single-flight, so the MAX(idx)+1 read
// needs no extra locking.
func (s *Store) AddStep(ctx context.Context, st RunStep) error {
	if st.StartedAt.IsZero() {
		return fmt.Errorf("add step to run %d: started_at required", st.RunID)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO run_steps (run_id, idx, kind, started_at, finished_at, status, summary)
		VALUES (?, `+nextStepIdx+`, ?, ?, ?, ?, ?)`,
		st.RunID, st.RunID, st.Kind, unixNano(st.StartedAt), nullTime(st.FinishedAt), st.Status, st.Summary)
	if err != nil {
		return fmt.Errorf("add step to run %d: %w", st.RunID, err)
	}
	return nil
}

func (s *Store) AddEvidence(ctx context.Context, e Evidence) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO evidence (run_id, idx, check_kind, target, expected, actual, status, weak)
		VALUES (?, `+nextEvidIdx+`, ?, ?, ?, ?, ?, ?)`,
		e.RunID, e.RunID, e.CheckKind, e.Target, e.Expected, e.Actual, e.Status, boolToInt(e.Weak))
	if err != nil {
		return fmt.Errorf("add evidence to run %d: %w", e.RunID, err)
	}
	return nil
}

func (s *Store) AddArtifact(ctx context.Context, a Artifact) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO artifacts (run_id, idx, name, path, bytes)
		VALUES (?, `+nextArtifIdx+`, ?, ?, ?)`,
		a.RunID, a.RunID, a.Name, a.Path, a.Bytes)
	if err != nil {
		return fmt.Errorf("add artifact to run %d: %w", a.RunID, err)
	}
	return nil
}

func (s *Store) ListSteps(ctx context.Context, runID int64) ([]RunStep, error) {
	rows, err := s.db.QueryContext(ctx, stepsByRun, runID)
	if err != nil {
		return nil, fmt.Errorf("list steps for run %d: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []RunStep
	for rows.Next() {
		var (
			st       RunStep
			started  int64
			finished sql.NullInt64
		)
		if err := rows.Scan(&st.RunID, &st.Idx, &st.Kind, &started, &finished, &st.Status, &st.Summary); err != nil {
			return nil, fmt.Errorf("list steps for run %d: %w", runID, err)
		}
		st.StartedAt = timeFromUnixNano(started)
		st.FinishedAt = timeFromNull(finished)
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list steps for run %d: %w", runID, err)
	}
	return out, nil
}

func (s *Store) ListEvidence(ctx context.Context, runID int64) ([]Evidence, error) {
	rows, err := s.db.QueryContext(ctx, evidenceByRun, runID)
	if err != nil {
		return nil, fmt.Errorf("list evidence for run %d: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Evidence
	for rows.Next() {
		var (
			e    Evidence
			weak int64
		)
		if err := rows.Scan(&e.RunID, &e.Idx, &e.CheckKind, &e.Target, &e.Expected, &e.Actual, &e.Status, &weak); err != nil {
			return nil, fmt.Errorf("list evidence for run %d: %w", runID, err)
		}
		e.Weak = weak != 0
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list evidence for run %d: %w", runID, err)
	}
	return out, nil
}

func (s *Store) ListArtifacts(ctx context.Context, runID int64) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, artifactByRun, runID)
	if err != nil {
		return nil, fmt.Errorf("list artifacts for run %d: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.RunID, &a.Idx, &a.Name, &a.Path, &a.Bytes); err != nil {
			return nil, fmt.Errorf("list artifacts for run %d: %w", runID, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list artifacts for run %d: %w", runID, err)
	}
	return out, nil
}
