// Package orchestrate drives the run state machine
// (plan → levels in order → evidence → cleanup-always) and owns all evidence
// writing — the only package besides store that touches run records.
// See DESIGN.md §9.1.
package orchestrate
