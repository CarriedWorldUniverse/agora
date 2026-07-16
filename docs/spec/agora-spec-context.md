# agora spec — context management (integration seam only)

Closes coherence hole #3 (2026-07-15). **The algorithm is NOT spec'd here**: a new context-management model is under active testing in a separate thread (operator, 2026-07-15). This file fixes only the *seam* that model plugs into, so the rest of the harness can build against a stable contract regardless of what the testing concludes.

## 1. The interface

Per-thread `ContextManager`:

- `Assemble(thread, turn_input) → messages` — produces the model-visible context for a sampling request, within `ModelInfo.context_window` (bridle spec §1). Owns what goes in and in what compressed form.
- `Observe(usage)` — fed after every request (bridle `usage` event); the manager tracks pressure.
- `Compact(trigger: manual|auto) → CompactionResult` — invoked by `/compact` (manual) or by the manager's own pressure signal (auto). May be a no-op for models that manage continuously rather than in compaction episodes.

The default v1 implementation can be trivial (assemble-verbatim + warn at threshold); the new model replaces it behind the same interface.

## 2. Fixed contracts (hold for ANY algorithm)

1. **The persisted thread's existing items are never rewritten** (append-only; a compaction marker MAY be appended — persistence §1). Curation/summarization changes only the model-visible *assembly*, never past items; the thread store keeps full history (resume, fork, audit, and pod-handoff depend on this).
2. **Hooks fire**: `PreCompact {trigger}` (may abort via `continue:false`) and `PostCompact` — already in the hooks spec; the manager must route through them.
3. **Regenerated, not summarized**: tool schemas (core + session-loaded via tool_search), the skills catalog, AGENTS.md fragments, the MEMORY.md index fragment (agora-spec-memory §2), and the identity/mode system fragments are *re-emitted fresh* into the post-compaction assembly — they are state, not conversation, and must never pass through a summarizer.
4. **Wire events**: `thread.compaction.started {trigger}` / `.completed {tokens_before, tokens_after}` so frontends can show it (codex exec_events has the same pair). The algorithm MAY emit additional non-compaction view events (e.g. the curation spec's `thread.curation.demoted`/`readmitted` for view-only LRU that mutates no thread state) — enumerated in agora-spec-io alongside the compaction pair.
5. **Mid-work continuity**: a running turn is never interrupted by auto-compaction; the manager acts between sampling requests (codex's pre-sampling compact point) or between turns.
6. **Workflow journal and agent-graph are out of scope** — they are not model context and never compact.
7. `context_length` errors from bridle route to the manager (one recovery attempt: compact-and-retry) before failing the turn.
8. `/status` exposes the manager's numbers: effective window, current estimate, % remaining (TUI token display keys off this, not raw usage).

## 3. Pending from the other thread

~~When the new model's testing settles~~ **Settled 2026-07-15**: the testing thread concluded (curation beats memory stores, three model families, `bridle/wset` merged PR #78) and the algorithm is spec'd in **`agora-spec-context-curation.md`** — working-set ledger (one live copy per artifact key, budget+LRU with hot-set immunity and hysteresis), assistant-side write-arg supersession, staleness invalidation, summarization demoted to last resort. It keeps no persistent artifacts (ledger rebuildable by thread replay). Contracts in §2 remain non-negotiable and are mapped point-by-point in that file's §7.
