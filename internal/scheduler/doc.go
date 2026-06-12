// Package scheduler runs drills on cron or human-shorthand schedules with
// jitter, global single-flight, per-run timeouts, and Proof-SLA staleness
// computation. Time-dependent logic takes an injected clock. See DESIGN.md §9.6.
package scheduler
