// Package turnengine: the engine adapter — agora's io.Engine implemented
// over a *bridle.Harness.
//
// Build units: Phase 2 U-C1 (agora-engine-blueprint.md; NEX-777) proved the
// architecture with a text-only slice: drive ONE deliberation turn through
// bridle's Harness and stream it as agora's contracts.Event vocabulary.
// U-C2 (NEX-778) wires in the Phase 1 tool surface (internal/toolrunner):
// TurnRequest.Tools now carries real fs/exec specs and tool calls execute
// via surfaceRunner. U-C3 (NEX-781) gates that execution: a BeforeToolCall
// hook classifies every call (internal/toolrunner.Classify) and resolves it
// through internal/approval.Decide (reused verbatim), blocking the turn
// goroutine on an interactive rendezvous for Ask outcomes until an
// approval_response Input resolves it. U-C6/U-C7 (NEX-785) add optional
// durability: a WithStore(contracts.ThreadStore) Option (nil by default —
// no persistence) Appends each succeeded turn's ThreadItems at the turn
// boundary, and gives the claude-sdk lane a stable per-thread
// bridle.SessionHandle (id = threadID, New computed once from a
// first-turn prior-items probe) so continuations RESUME the Claude
// conversation instead of restarting it — see manager.go's WithStore/
// turnSession/persistTurn doc comments. Approval-decision persistence
// (TIApprovalRequest/TIApprovalDecision) is deferred past this unit — see
// persistTurn's doc comment. ctxmap and the in-process launch path are
// ALL LATER build units (U-D*, U-E*) — this package deliberately does not
// touch either; see manager.go's package-level doc comment for the exact
// scope line. plan/question special-casing is also out of THIS unit's
// scope: no plan/question tools exist on the fs/exec surface Classify
// covers today.
//
// Layout:
//
//	manager.go — Manager: contracts-facing io.Engine, the turn state
//	             machine (one in-flight turn at a time, interrupt/end
//	             handling), TurnRequest construction, the toolrunner.Surface
//	             construction + WithRoots default, and (U-C6/U-C7)
//	             WithStore/turnSession/persistTurn.
//	approval.go — the U-C3 approval gate: the BeforeToolCall hook, the
//	             Ask rendezvous (registerWaiter/resolveWaiter + the
//	             turnHookCtx set/cleared per turn), scopeKeyFor/
//	             recordScopeGrant, and defaultPolicy.
//	surfacerunner.go — surfaceRunner: bridle.ToolRunner implemented over a
//	             *toolrunner.Surface (the tool-error-vs-Go-error mapping),
//	             plus toolDefsFromSpecs (contracts.ToolSpec -> bridle.ToolDef).
//	sink.go     — turnSink: bridle.EventSink -> contracts.Event translation
//	             for this slice's subset (ModelChunk/ReasoningChunk/
//	             TurnDone/TurnError/Warning).
//	ids.go      — IDGen: injectable turn-id minting (deterministic for
//	             tests, mirrors ctxmgr's Clock injection pattern).
//	hookrunner.go — HookRunner: discovers hooks.json (user+project layers),
//	             resolves trust/enable state, and dispatches handlers over a
//	             real shell-exec RunFunc (internal/hooks itself never spawns
//	             a process — this is that seam's production implementation).
//	hooks_wire.go — WHERE each of the 10 lifecycle-hooks events fires on the
//	             live turn path (PreToolUse/PermissionRequest/PostToolUse/
//	             SessionStart/UserPromptSubmit/Stop) — see its file-level
//	             doc comment, and DEVIATIONS.md #13 for this unit's scope.
//
// Provider injection: production callers construct a Manager over
// provider/claudesdk.New() (funnel mode); tests construct one over
// bridle/fake.NewProvider(steps...) — NEVER against the real sidecar.
package turnengine
