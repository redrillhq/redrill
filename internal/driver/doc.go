// Package driver defines the SourceDriver interface; engine implementations
// live in subpackages (borg, dumpdir, restic). Drivers are read-only on
// repositories by construction: no write/prune/delete code path may ever
// exist here. See DESIGN.md §9.2.
package driver
