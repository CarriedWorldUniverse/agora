// Package hooks — the 10-event lifecycle hooks engine.
//
// Build unit: U9 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-hooks.md.
//
// This package models the ENGINE: config parsing, matcher semantics,
// discovery/layering, trust-hash gating, per-event I/O contracts (parse
// stdin/stdout per the exit-code convention), aggregation across matched
// handlers, and async dispatch. It does NOT execute shell processes itself —
// that is the daemon's job (spec §3 "Invocation"); this package exposes the
// RunFunc seam (dispatch.go) so the daemon can plug in a real fork/exec
// while this package's own tests never spawn a process (ground rule 6).
//
// File map:
//
//	events.go    — the 10 EventName constants + matcher-ignored/turn-scope facts (§1.2, §3).
//	config.go    — hooks.json/TOML shape: Handler, MatcherGroup, EventMap, Config (§1.1–§1.4).
//	matcher.go   — matcher semantics: exact/alternation vs unanchored regex (§1.5).
//	discover.go  — layering, discovery order, positional keys (§4.1–§4.2).
//	trust.go     — content hash + trust/enable resolution, fail-closed (§4.4).
//	contract.go  — per-event stdin/stdout shapes + the exit-code convention (§2, top + §2.1–§2.10).
//	aggregate.go — per-event aggregation across matched handlers (§2.1–§2.3, §2.9–§2.10, §3).
//	dispatch.go  — Clock/RunFunc seams, sync+async dispatch, non-blocking async (§1.4 async, §3).
//	bridge.go    — the PermissionRequest ↔ approval-invariant seam (agora-spec-approvals.md §4 invariant 1).
package hooks
