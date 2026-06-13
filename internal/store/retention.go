package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Constant queries (no runtime string building) keep retention gosec-clean.
const (
	pruneByAge   = `DELETE FROM runs WHERE drill = ? AND started_at < ?`
	pruneByCount = `DELETE FROM runs WHERE drill = ? AND id NOT IN (
		SELECT id FROM runs WHERE drill = ? ORDER BY id DESC LIMIT ?)`
	pruneByAgeAndCount = `DELETE FROM runs WHERE drill = ? AND (
		started_at < ? OR id NOT IN (
			SELECT id FROM runs WHERE drill = ? ORDER BY id DESC LIMIT ?))`
)

// Prune deletes a drill's runs that fall outside the retention window and
// returns how many were removed. A run is pruned if it is older than maxAge OR
// beyond the newest maxCount — both caps apply, bounding storage by age and by
// count. A non-positive maxAge or maxCount disables that bound; with both
// disabled Prune is a no-op. Cascading foreign keys remove the runs' steps,
// evidence, and artifacts; drill_state is never touched (proof history is kept
// forever, DESIGN §9.3).
func (s *Store) Prune(ctx context.Context, drill string, maxAge time.Duration, maxCount int, now time.Time) (int64, error) {
	ageOn := maxAge > 0
	countOn := maxCount > 0
	if ageOn && now.IsZero() {
		return 0, fmt.Errorf("prune %s: now required for age-based retention", drill)
	}

	var (
		res sql.Result
		err error
	)
	switch {
	case ageOn && countOn:
		res, err = s.db.ExecContext(ctx, pruneByAgeAndCount, drill, unixNano(now.Add(-maxAge)), drill, maxCount)
	case ageOn:
		res, err = s.db.ExecContext(ctx, pruneByAge, drill, unixNano(now.Add(-maxAge)))
	case countOn:
		res, err = s.db.ExecContext(ctx, pruneByCount, drill, drill, maxCount)
	default:
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("prune %s: %w", drill, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune %s: %w", drill, err)
	}
	return n, nil
}
