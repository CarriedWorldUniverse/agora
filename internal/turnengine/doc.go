// Package turnengine: the engine adapter — agora's io.Engine implemented
// over a *bridle.Harness.
//
// Build unit: Phase 2 U-C1 (agora-engine-blueprint.md; NEX-777). This is
// the architecture-proving FIRST slice: drive ONE text deliberation turn
// through bridle's Harness and stream it as agora's contracts.Event
// vocabulary. Tools, approvals, ctxmap, persistence, and the in-process
// launch path are ALL LATER build units (U-C2..U-C7, U-D*, U-E*) — this
// package deliberately does not touch any of them; see manager.go's
// package-level doc comment for the exact scope line.
//
// Layout:
//
//	manager.go — Manager: contracts-facing io.Engine, the turn state
//	             machine (one in-flight turn at a time, interrupt/end
//	             handling), TurnRequest construction.
//	sink.go     — turnSink: bridle.EventSink -> contracts.Event translation
//	             for this slice's subset (ModelChunk/ReasoningChunk/
//	             TurnDone/TurnError/Warning).
//	ids.go      — IDGen: injectable turn-id minting (deterministic for
//	             tests, mirrors ctxmgr's Clock injection pattern).
//	toolrunner.go — noopToolRunner: the fake provider (and this slice's
//	             text-only turns) never calls a tool, but RunTurn's
//	             signature requires a bridle.ToolRunner.
//
// Provider injection: production callers construct a Manager over
// provider/claudesdk.New() (funnel mode); tests construct one over
// bridle/fake.NewProvider(steps...) — NEVER against the real sidecar.
package turnengine
