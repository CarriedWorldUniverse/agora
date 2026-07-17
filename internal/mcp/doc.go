// Package mcp implements the MCP manager + native tool-family runtime: per-
// server config schema, eager concurrent startup with required-server
// gating, the tool catalog cache, model-visible tool naming, deferred-tool
// registry + tool_search, OAuth credential storage/refresh/login state
// machine, and the fs-watcher staleness signal the fs/exec native families
// share.
//
// Build unit: U8 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-mcp.md §1-5a. §1a (wasm transport) is v1.1 and
// deliberately out of scope for this unit.
//
// This package builds on the compiled seams in the contracts package
// (contracts.ToolSpec, contracts.ToolState, contracts.FSChange,
// contracts.QuestionSource, contracts.ApprovalKind) — it does not redefine
// those types. The turn engine and approvals wiring that CONSUME these
// tools (dispatching an approved MCP tool call, routing an elicitation
// through the approval pipeline) are other units' work; this package
// exposes the seams (Manager, Registry, fs-watcher events) they wire
// against.
package mcp
