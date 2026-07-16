// Package contracts is the compiled form of the agora spec set's seams.
//
// Every exported symbol cites the spec chapter and section it implements
// (docs/spec/agora-spec-*.md once U1 lands them in-repo). Units build against
// these types; a divergent reading of the spec should fail here, at compile
// time, not at integration.
//
// Seam map:
//
//	identity.go   — agora-spec.md §Identity
//	approval.go   — agora-spec-approvals.md
//	capability.go — agora-spec-remote.md §4 (controller capability tiers)
//	question.go   — agora-spec-planning-questions.md §4–§6
//	plan.go       — agora-spec-planning-questions.md §1–§3
//	event.go      — agora-spec-io.md §1–§2 (events + input messages)
//	thread.go     — agora-spec-persistence.md
//	context.go    — agora-spec-context.md §1–§2
//	model.go      — agora-spec-bridle.md (what agora requires of bridle)
//	tool.go       — agora-spec-mcp.md §5–§5a (registry surface)
//	prompt.go     — agora-spec-prompt.md §1–§2a
//	provision.go  — agora-spec-remote.md §6a
//
// The JSON schemas under /schemas mirror the wire-visible subset for
// non-Go clients; the Go types here are the source of truth.
package contracts
