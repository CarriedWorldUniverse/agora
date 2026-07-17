// Package workflow — the starlark workflow engine: agent/parallel/pipeline,
// journal/resume, run parking.
//
// Build unit: U14 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-workflows.md.
//
// Implemented at U14 atop U1's compiled contracts seams, U10's subagent
// primitive (internal/subagent — AgentInvoker/SubagentInvoker map
// ctx.agent() onto a real *subagent.Manager), and U11's escalation ladder
// (internal/planning — QuestionRouter routes ctx.question/ctx.approval
// through planning.QuestionLog, never a bespoke path).
//
// Determinism (spec §0/§4): the go.starlark.net interpreter gives no
// ambient clock/randomness/IO; the engine adds nothing that could — no
// wall-clock read anywhere in Run, only the caller-injected Clock (see
// clock.go). Journal replay keys are (Branch, LocalSeq, Kind) rather than a
// flat run-wide counter (see journal.go's Entry doc comment) so that
// ctx.parallel/ctx.pipeline's genuinely concurrent goroutines still produce
// a deterministic, content-addressed cache regardless of Go's scheduler.
//
// Scope carried by this unit, per the build brief: the starlark host API
// (ctx.agent/parallel/pipeline/log/phase/question/approval/args/now),
// scheduler + concurrency/lifetime/item caps, journal/resume, and run
// parking. Deliberately NOT in scope (spec §7 v1 sizing / build-plan
// boundary): budget enforcement (ctx.budget is a stub, canon.go/ctx.go),
// nested ctx.workflow() invocation (returns an honest "deferred" error),
// bridle's model-alias registry (model/effort strings pass through
// unresolved — see agent.go), args_schema validation (stored, not
// enforced), and the CLI/TUI surface (`agora workflow run/ps/watch`,
// io/tui units).
package workflow
