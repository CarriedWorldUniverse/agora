// Package memory implements agora's v1 file-based, identity-scoped memory:
// a per-identity directory of one-fact-per-file notes plus an atomically
// maintained MEMORY.md index, the memory.* tool family operations over
// that store, and the developer-role index-fragment renderer used at
// prompt-assembly time.
//
// Build unit: U13 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-memory.md.
//
// Memory content is REFERENCE, never instruction-weight authority
// (agora-spec-memory.md §2, §4): the index is injected as a
// harness-generated developer-role catalog (the same class as the skills
// catalog, agora-spec-prompt.md §1a — see internal/prompt.RoleMap's
// FragMemoryIndex entry), and individual memory bodies are read by the
// agent on demand, never auto-injected at any authority weight.
package memory
