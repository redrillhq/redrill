// Package checks defines the Check interface and one file per check kind,
// each producing Evidence (expected/actual/status/weak). A false predicate is
// a fail; an unevaluable check is an error — never conflate the two.
// See DESIGN.md §7.
package checks
