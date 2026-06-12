// Package store is the embedded SQLite state (modernc.org/sqlite, cgo-free):
// forward-only migrations, run/step/evidence records, retention pruning, all
// timestamps UTC. The only package that may import a SQL driver.
// See DESIGN.md §9.3.
package store
