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

	// ErrUnknownAlias: an unresolvable model alias — spec §2a: "unresolvable
	// alias = error at run start, not mid-run." Raised by callers wiring an
	// alias resolver; the bare engine does not resolve aliases itself (see
	// doc.go — that is bridle's registry, out of this unit's scope).
	ErrUnknownAlias = errors.New("workflow: unresolvable model alias")
)
