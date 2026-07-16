# agora spec — memory (v1: file-based, identity-scoped)

Closes the memory hole (2026-07-15). Decision: v1 ships a **minimal file-based memory** — the pattern proven by shadow's own MEMORY.md workflow — and codex's background memories pipeline stays parked (revisit only if the file pattern hits limits). This is what `tools = [..., "memory", ...]` in profile.chat refers to.

## 1. Store

Per-identity: `~/.agora/memory/<identity-name>/` (name, not fingerprint, for human editability — the dir is operator-readable/writable like any notes dir):

- `MEMORY.md` — the index: one line per memory, `- [title](file.md) — hook`. Injected at thread start.
- `<slug>.md` — one fact per file, frontmatter `{name, description, type: user|feedback|project|reference}`, body = the fact (+ why/how-to-apply for feedback/project). `[[name]]` links between memories.

Identical to the Claude Code memory format shadow already runs — existing memory dirs are usable as-is.

## 2. Injection

- `MEMORY.md` content injected as a **developer-role fragment** — same class as the skills catalog: a harness-generated CATALOG, not constitution and not content (role map: agora-spec-prompt §1a; regenerated per assembly per context spec contract #3), under a token budget (same 2%-class budgeting as the skills catalog; truncate whole lines, newest-first survives).
- Individual memory files are NOT auto-injected — the agent reads them by relevance (progressive disclosure again). Recalled content is background context, not instructions, and **never instruction-weight authority** (operator, 2026-07-16): memories are point-in-time observations that can be stale or wrong — elevating their authority turns errors into drift the model defends instead of checks (the live case: non-canon still being found inside the Carried World canon). Verify recalled claims against ground truth (tools) before acting on them.

## 3. The `memory` tool family

`memory.read(name)` / `memory.write(name, frontmatter, body)` / `memory.list()` / `memory.delete(name)` — scoped to the identity's memory dir. Writes go through the tools (auto-updating the MEMORY.md index line atomically — fixing the read-before-edit race shadow's harness suffers); general fs read-everywhere covers reads too, but the tools keep the index consistent. Write outside the memory dir via these tools = impossible by construction; the dir sits outside the wd sandbox write scope but the family carries its own grant.

## 4. Deliberately absent in v1

Background extraction/consolidation pipelines (codex memories), embedding recall, cross-identity shared memory (that's what comms/commonplace are for), memory sync (a future casket concern). The tool family + format are the stable surface; smarter recall slots behind them later.
