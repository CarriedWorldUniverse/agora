package workflow

import "errors"

// Sentinel errors, checkable via errors.Is regardless of which code path
// produced them (house style, mirrors internal/subagent, internal/planning).
var (
	// ErrNoMain: the script has no top-level def main(ctx, args).
	ErrNoMain = errors.New("workflow: script has no main(ctx, args) function")
	// ErrNoMeta: the script never called workflow_meta(...).
	ErrNoMeta = errors.New("workflow: script never assigned a workflow_meta(...) result")
	// ErrMetaEmptyName: workflow_meta(name=...) is required and non-empty.
	ErrMetaEmptyName = errors.New("workflow: workflow_meta name is required and must be non-empty")

	// ErrLifetimeCapExceeded: a run's ctx.agent() calls exceeded the
	// per-run lifetime cap (spec §3: "Lifetime cap per run (e.g. 1000
	// agents) as a runaway backstop").
	ErrLifetimeCapExceeded = errors.New("workflow: run exceeded its lifetime agent cap")
	// ErrItemCapExceeded: a single ctx.parallel/ctx.pipeline call exceeded
	// the per-call item cap (spec §3: "per-call item cap (e.g. 4096)").
	ErrItemCapExceeded = errors.New("workflow: parallel/pipeline call exceeded the per-call item cap")

	// ErrLifetimeBranchCapExceeded: a run's total ctx.parallel/ctx.pipeline
	// goroutine spawns (across all nesting depth, the whole run's lifetime)
	// exceeded Config.LifetimeBranchCap — the backstop against a
	// recursively-fanning script spawning goroutines faster than any single
	// PerCallItemCap or agent cap would catch (review finding: "nested
	// parallel/pipeline goroutine explosion").
	ErrLifetimeBranchCapExceeded = errors.New("workflow: run exceeded its lifetime branch-spawn cap")

	// ErrMaxDepthExceeded: a starlark value's nesting depth exceeded the
	// engine's native-recursion guard (toGo/canonicalize) — the backstop
	// against a script-controlled value driving unbounded Go-native
	// recursion into a fatal, unrecoverable stack overflow (review finding:
	// "unbounded native-Go recursion -> process crash").
	ErrMaxDepthExceeded = errors.New("workflow: value nesting exceeds the engine's depth guard")

	// ErrUnknownAlias: an unresolvable model alias — spec §2a: "unresolvable
	// alias = error at run start, not mid-run." Raised by callers wiring an
	// alias resolver; the bare engine does not resolve aliases itself (see
	// doc.go — that is bridle's registry, out of this unit's scope).
	ErrUnknownAlias = errors.New("workflow: unresolvable model alias")
)
