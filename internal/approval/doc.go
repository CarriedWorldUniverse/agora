// Package approval implements the decision pipeline described in
// docs/spec/agora-spec-approvals.md §2/§4: given a request (kind + scope
// context) and a policy set (contracts.PolicySet — a named preset or a
// custom one), Decide resolves it to allow / deny / ask / convert, checking
// any previously scoped allow first so a repeated in-scope request never
// asks twice.
//
// This unit (U7) does not implement stages 3+ of the canonical pipeline
// (hooks → policy → approvers → queue → timeout, remote §8): hooks,
// approver fan-out, the request queue and timeout handling are consumed by
// other units (remote/MCP/TUI). Decide covers stage 2 (policy) plus the
// scope short-circuit that stage 3's approver answers create — the pure,
// policy-shaped decision this package owns. Kinds, decisions, scopes,
// policy values, and the built-in presets themselves are the contracts
// package's types (contracts/approval.go); this package builds the engine
// on top of them, it does not redefine them.
//
// Build unit: U7 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-approvals.md.
package approval
