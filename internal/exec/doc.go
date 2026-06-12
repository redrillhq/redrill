// Package exec is the multi-host seam: the Executor interface with
// serializable StepSpec/StepResult, and LocalExecutor, the only v1
// implementation. StepSpec/StepResult must stay serializable (no func fields,
// channels, or DB handles) so Phase 4 agents are an additive transport.
// See DESIGN.md §9.4.
package exec
