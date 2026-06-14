// Package redact scrubs known secret values and *_PASSWORD-style environment
// variables from captured output. It is the mandatory boundary before anything
// becomes evidence or logs. Leaf package: it must not import any other
// redrill package (enforced by depguard). See DESIGN.md §9.7.
package redact
