// Package config holds the typed configuration schema, strict YAML parsing
// (unknown keys are errors), duration/size parsing, and *_file/*_env secret
// references. Leaf package: it must not import any other redrill package
// (enforced by depguard). See DESIGN.md §7.
package config
