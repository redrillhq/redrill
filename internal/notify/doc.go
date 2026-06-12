// Package notify dispatches events (fail/error/recover/stale/weekly_digest)
// via shoutrrr. Messages lead with the diagnosis and the last-good date, and
// keep fail (backup is bad) visibly distinct from error (auditor is blind).
// See DESIGN.md §8.4.
package notify
