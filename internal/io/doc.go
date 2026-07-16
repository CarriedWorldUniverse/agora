// Package io — daemon + pipe mode + session protocol; the seam every frontend consumes.
//
// Build unit: U2 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-io.md.
//
// This package builds on the wire types in contracts (Event, Input,
// Capability, ...) and adds the mechanics the spec assigns to U2:
//
//   - Engine: the minimal interface this package drives. The real turn
//     engine (model calls, tool loop, context assembly) is a later build
//     unit; io only needs "consume Input, produce Event" to do its job.
//     ScriptedEngine is a deterministic stub implementation for tests.
//   - RunPipe: pipe mode (§1) — stdin JSONL Input -> Engine -> stdout JSONL
//     Event, with --filter/--deltas behavior and §4 exit codes.
//   - Session: the daemon-side multi-attach hub (§0a) — fan-out to every
//     attached client, first-answer-wins arbitration on approval/question
//     responses, presence events, and replay for late attachers.
//   - ClientFrame/ServerFrame + ServeConn: the session protocol (§2) framing
//     over any connection (unix socket or ws — both are byte streams once a
//     websocket.Conn is wrapped with coder/websocket's NetConn adapter, so
//     one codec serves both transports).
//
// The daemon binary that binds real sockets, owns the thread store, and
// wires a real Engine in is U18 (internal/daemon) — this package supplies
// the mechanics, not the process.
package io
