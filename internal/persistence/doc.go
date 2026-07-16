// Package persistence — JSONL source-of-truth + SQLite mirror; the ThreadStore implementation.
//
// Build unit: U3 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-persistence.md.
//
// Two implementations of contracts.ThreadStore:
//   - LocalStore: JSONL files under <root>/threads/<yyyy-mm>/<thread_id>.jsonl
//     (source of truth, never rewritten) + a SQLite mirror at <root>/state.db
//     (queries only, rebuildable via RebuildIndex). Spec §1, §2.
//   - MemStore: pure in-memory, for tests and ephemeral pods (persist=false).
//     Spec §3.
//
// Both take their root/state injected via constructor (NewLocalStore(root,
// cfg), NewMemStore()) — callers (including tests) choose the directory;
// nothing here defaults to the real ~/.agora.
package persistence
