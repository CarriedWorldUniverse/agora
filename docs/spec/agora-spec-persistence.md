# agora spec — thread persistence

Closes the persistence hole (2026-07-15, operator accepted the recommendation): adopt **codex's three-layer shape** — append-only JSONL as source of truth, SQLite mirror for queries, storage-neutral store interface. Extracted reference: codex `rollout/` (recorder, reverse scanner), `state/` (sqlite mirror), `thread-store/` (ThreadStore trait, local + in-memory impls).

## 1. JSONL — the source of truth

One file per thread: `~/.agora/threads/<yyyy-mm>/<thread_id>.jsonl` (month sharding keeps dirs sane).

- **Line 1 = meta**: `{thread_id, created_at, identity_fp, identity_name, profile, working_dir, project_root, parent_thread?, fork_of? {thread_id, seq}, title?}`. Working-dir updates (`/cd`, resume-switch) append a `wd_changed` item — the *latest* wins at resume; the meta line is never rewritten.
- **Then items, append-only**: user/agent messages, reasoning, tool calls + results, approvals (request + decision + stage + actor), questions + answers and park/resume markers (planning-questions §5/§7), plan revisions (each `plan` tool update = a new item, never rewritten), hook outcomes, compaction markers, wd changes, provisioning events. Every item: `{seq, ts, type, identity/device attribution, payload}`.
- **Never rewritten.** Compaction adds a marker item; it never edits history (context spec contract #1). Crash safety: append + fsync on turn boundaries (config: per-item for paranoid mode).
- **`ts` = EVENT time, not persist time (NEX-796, session-log finding 2026-07-20).** Items are stamped when the event is emitted (sink/turnengine), carried into persistence — batch-writing at the turn boundary must not batch-stamp. Measured failure: a 39-tool-call turn's items all shared the turn-end timestamp, showing a multi-minute agentic turn as 71 seconds and making intra-turn timing/session analytics unrecoverable.
- **Per-turn usage persists (NEX-796).** The `turn.completed` usage payload (input/output/cached/reasoning tokens + cost, NEX-794 shape) is recorded at the turn boundary — a `turn_usage` item (or equivalent field on the turn's closing item) — so ccusage-style session/cost history is reconstructable from the JSONL alone, not just observable live on the status row.
- **Resume** = replay (tail-first via reverse scan for fast open; full replay for context reconstruction). **Fork** (backtrack, `/fork`) = new file with `fork_of {thread_id, seq}` — no copying; reads chain through the parent up to the fork point.
- Subagent children are ordinary threads (own files) with `parent_thread` set; the agent graph edges live in SQLite (§2). Workflow runs keep their own journal dir (workflows spec §4) and reference thread ids — never merged.

## 2. SQLite mirror — queries only, rebuildable

`~/.agora/state.db`, always derivable from the JSONL (a `rebuild-index` command regenerates it; corruption is an inconvenience, never data loss):

- `threads(id, created_at, updated_at, identity_fp, profile, working_dir, project_root, title, archived, parent_thread, fork_of)` — indexed on `working_dir`/`project_root` (the `/resume` wd filter), `updated_at` (recency), `identity_fp`.
- `agent_edges(parent_thread, child_thread, status open|closed)` — the agent-graph store (subagents spec §3).
- `items_fts` — optional FTS5 over message text for `/resume` search.
- Device enrollments, hook trust state, and approval `session`-scope grants also persist here (daemon state, same rebuild-from-source rules do NOT apply to these — they're primary; small, backed up with the identity dir).

## 3. Store interface (Go)

`ThreadStore`: `Create(meta) / Resume(id) → replay iterator / Append(id, items) / Meta(id) / List(filter{wd, identity, archived, text}) / Fork(id, seq) / Archive / Delete`. Implementations: `LocalStore` (JSONL+SQLite, above) and `MemStore` (tests, ephemeral pods with `persist=false`). Everything above (daemon, io seam, pods) talks to the interface — a future casket/broker-synced store is an implementation, not a redesign.

## 4. Retention

Nothing auto-deletes. `Archive` = flag (hidden from default `/resume`). `Delete` = file removal + index purge, TUI-confirmed. Pod `deprovision` deletes only threads created with `ephemeral: true` in provisioning; provisioned `session.resume` threads belong to the store the broker points at.
