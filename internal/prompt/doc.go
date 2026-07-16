// Package prompt — system-prompt composition: segments, core packages, dialects/renditions.
//
// Build unit: U4 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-prompt.md.
//
// Layout:
//
//	role.go     — §1a fragment role map, exposed as data
//	segment.go  — §1 segment order + profile/identity/environment types
//	core.go     — CorePackage, on-disk load, section split, content hash
//	builtin.go  — the embedded built-in core (§2)
//	toml.go     — a tiny hand-rolled TOML subset for manifest/dialects files
//	resolve.go  — §2a precedence: built-in < system override < user override
//	              < named variant; refuses project/dispatch layers
//	dialect.go  — §4 dialect knobs + rendition selection
//	compose.go  — §3 the deterministic render pipeline (Compose)
//	verbs.go    — `agora prompt …` backends (New/Show/Check/Rebase/Compile)
//
// The built-in core.md shipped here is a small, structurally-complete
// placeholder (real core prose is a later interactive/eval task, §6).
package prompt
