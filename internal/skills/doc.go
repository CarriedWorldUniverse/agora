// Package skills — skill discovery, catalog injection, $mention; + AGENTS.md merge (subagents §6).
//
// Build unit: U5 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-skills.md.
//
// Implemented at U5:
//   - parse.go:    SKILL.md frontmatter (§1.1) + lenient-YAML repair,
//     agents/openai.yaml sidecar (§1.2).
//   - discover.go: roots, traversal guards, dedup, rank (§2).
//   - catalog.go:  catalog rendering + §3.2 budget fitting, §3.3
//     invocation fragment.
//   - mention.go:  $mention sigil extraction + resolution (§4).
//   - implicit.go: script-run / doc-read heuristic detection (§5).
//   - agentsmd.go: AGENTS.md discovery + merge (agora-spec-subagents.md
//     §6) — lives here since it's the companion contract
//     children inherit alongside skills.
//
// All FS-facing code takes an explicit, injectable []Root / cwd — no
// package-level global touches $HOME or the real filesystem; production
// wiring (DefaultRoots) is a thin caller on top.
package skills
