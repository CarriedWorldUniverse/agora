// Package ctxmgr — the ContextManager: working-set ledger, two-layer budget,
// curation.
//
// Build unit: U12 (docs/spec/agora-spec-build.md §1).
// Specs: docs/spec/agora-spec-context.md (the seam, §2 fixed contracts) and
// docs/spec/agora-spec-context-curation.md (the algorithm the seam plugs
// in behind; §7 maps every context-spec contract to how curation honors it).
//
// Layout:
//
//	clock.go     — injectable Clock (house pattern, mirrors internal/mcp)
//	config.go    — §6 tunables, defaults from the testing (TUNING itself is
//	               an interactive follow-on per agora-spec-build.md §0.5 —
//	               this unit only makes the knobs explicit and documented)
//	keys.go      — the (tool_class, key) artifact identity (§2)
//	ledger.go    — the working-set ledger: resident/tracked tiers, staleness,
//	               LRU eviction with hysteresis, re-admission (§2, §3)
//	spanindex.go — SpanIndexer seam + the line-window fallback (§3b partial
//	               re-admission)
//	estimator.go — chars/4 token estimate + Observe() correction factor (§3a)
//	hooks.go     — HookRunner seam for Pre/PostCompact (context §2 contract 2)
//	              — the real hook dispatch lives in internal/hooks (U9); this
//	              is the narrow interface ctxmgr calls through, satisfied by
//	              a hooks-package adapter the turn-engine unit wires up
//	events.go    — thread.curation.{demoted,readmitted} payload shapes
//	              (contracts.EvCurationDemoted/EvCurationReadmitted carry these)
//	payload.go   — the tool_call/tool_result payload shape ctxmgr reads keys
//	              from (persistence leaves ThreadItem.Payload as `any` with
//	              no fixed tool-item schema yet — see payload.go doc comment
//	              for the interpretation this unit takes)
//	manager.go   — Manager: contracts.ContextManager (Assemble/Observe/
//	              Compact/Status)
//
// Scope boundary (per the build ground rules): this unit implements the
// ALGORITHM and the §7 contract-compliance tests. The turn engine that
// feeds Manager real per-turn ThreadItems, wires a live HookRunner, and
// drives ApplyFSChange from U8's fs-watcher between turns is another unit's
// integration work; here those are seams (interfaces) with a working
// default (no-op hooks, in-memory ledger, line-window span fallback).
package ctxmgr
