// Package planning — the plan tool/gate + question tool + the escalation ladder.
//
// Build unit: U11 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-planning-questions.md.
//
// This package models the STATE MACHINE and durable state only — the
// conversational-register UI, the actual question-asking transport, the
// workflow engine (U14), and the nexus-side dispatch needs_input contract
// are other units (or later work); their seams are noted in doc comments,
// not built here.
//
//   - ladder.go: the escalation ladder (§5) — Resolve maps
//     (QuestionContext, blocking) to a Disposition (park / die-honestly /
//     bubble / queue); Terminate builds the one-shot pod termination shape
//     (blocked: needs-input, §5/§8).
//   - plan.go: the plan artifact's append-only revision log (PlanLog, §1)
//     and the plan gate (Gate, §3) — an approval kind that NEVER allows
//     exit while open_questions is non-empty (§6 invariant 6), regardless
//     of operator intent.
//   - park.go: waiting-on-answer durable state (ParkLog) — Park/Resume
//     append items to a contracts.ThreadStore (internal/persistence's
//     LocalStore for real durability, MemStore for tests/ephemeral pods);
//     IsWaiting reconstructs current state by REPLAY, so a daemon restart
//     loses nothing but the store itself (§6 invariant 2).
//   - question.go: QuestionLog ties Ask (ladder resolution + persistence)
//     and Answer (attributed resolution, never fabricated — §6 invariants
//     1/4/5) together into the `question` tool's tool/item surface.
//
// Builds against the compiled seams in the contracts package
// (contracts/plan.go, contracts/question.go, contracts/approval.go) and the
// merged internal/persistence (durable state) and internal/approval (the
// plan gate rides the approvals pipeline as KindPlan; a question under
// PolicyConvert routes here).
package planning
