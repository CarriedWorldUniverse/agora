# agora spec — context curation (the algorithm behind the ContextManager seam)

2026-07-15. Fills §3 of `agora-spec-context.md`: the retention/assembly policy, from
the ctxmap/wset testing thread (agora `docs/ctxmap.md` arms A–F; `bridle/wset`
merged PR #78). Slots behind the §1 interface; every §2 contract is honored
(compliance map in §7 below).

> **Reference note, 2026-07-29.** The `docs/ctxmap.md` citation above now resolves
> on `main`. It was written on the `ctxmap-harness` branch, which was never merged,
> so this spec's evidence base pointed at a file that did not exist here — see its
> status header for what shipped and what did not.
>
> The `bridle/wset` pointer is **historical and no longer resolves**. Arm F shipped
> in this repo as `internal/ctxmgr` — a reimplementation, not an import — so the
> bridle package never acquired a consumer and was deleted in bridle PR #96. It
> remains readable in that repo's history at PR #78.
>
> Nothing was lost: the arm-F design is recorded in §0 and §§2–3 below, the
> evidence in `docs/ctxmap.md` §3, and the running code is `internal/ctxmgr`, wired
> by `internal/turnengine/ctxcuration.go`. **This spec is now the reference.**

## 0. Evidence base (why THIS algorithm)

Six experimental arms, three model families (DeepSeek v4-pro, GLM-5.2, Sonnet
4.6), forced context-degradation regime. Memory stores of every shape FAILED to
rescue agentic coding — extracted facts, working-state blocks, a symbol server,
agent-authored decision journals — with one shared mechanism: **models are
RL-trained to treat tool results as the only truth**; they re-verify via tools
rather than trust any injected block, and every protocol-following ask got
token-thin compliance. What worked was pure curation (arm F): keep the newest
copy of what the model reaches for, age out the rest. 12/12 passes vs 4/12
bare across the three families, at 4–20× FEWER tokens (re-reading was always
paid in tokens too). The five rules the algorithm embodies:

1. Keep the newest truth of anything the model re-verifies.
2. Age out noise aggressively — the enemy is indiscriminate eviction, not eviction.
3. Deliver information where the model looks (tool results), never system-prompt blocks.
4. Never make the model do the bookkeeping — the harness curates, the model works.
5. Keep the assembly prefix-stable so provider caching pays for what's retained.

## 1. The model: assembly is a projection

`Assemble(thread, turn_input)` renders the model-visible messages from the
append-only thread (persistence spec: the thread is NEVER mutated — this
algorithm is a view, idempotent by construction). Four tiers, in order:

1. **State fragments** — system prompt, tool schemas, skills catalog, AGENTS.md,
   MEMORY.md index, identity/mode. Regenerated fresh every assembly (context
   contract #3); marked as the cache-stable prefix (`cache_hints`, bridle §3).
2. **Conversation** — user/assistant TEXT, verbatim. Not curated in v1
   (dialogue is the thread; coding threads are tool-dominated, and the
   dialogue-side fact store is a separate, already-proven system).
3. **Working set** — the newest copy of each *keyed artifact* (below), budgeted.
4. **Recent window** — the last `keep_others` non-keyed tool results verbatim,
   size-capped; older ones stubbed.

## 2. The working-set ledger (item 2 unified with item 1)

The unit of retention is the **key** — an artifact identity, not a message:
`(tool_class, key)`, e.g. `(file, "src/a.py")`. Config maps tool names into
classes: reads (`Read→file_path`), full-content writes (`Write→file_path`),
mutations-without-content (edit/patch tools), commands (unkeyed). Per key the
ledger tracks:

- **live copy** — the single message currently carrying the key's content:
  the newest full read *or* full-content write (a write's ARGS are the newest
  truth of the file — the model authored it). All older reads AND older write
  args for the same key are **superseded** (stubbed / args-rewritten, §4).
- **content hash** — of the live copy's bytes. Shared datum for the edit-guard
  staleness check (mcp §5a: current on-disk hash vs this) and the
  identical-rewrite no-op test (§4). Kept for resident AND tracked keys, so the
  hash exists for the hot keys the edit-guard actually targets.
- **last-touch step** — most recent step that read, wrote, edited, or named
  the key. Drives LRU.
- **staleness** — a mutation that does NOT carry full content (edit tool,
  or any `run_command` the fs-watcher (agora-spec-mcp §5a) says touched the file) **invalidates**
  the live copy: it is stubbed as
  `[modified since this read: re-read for current content]` rather than
  retained. Retaining stale content as truth is worse than evicting —
  correctness rule, not a tuning knob. (cwlog's write-only tool surface never
  hit this; a full harness with edit tools hits it immediately.)

This resolves item 2 non-cosmetically: assistant `write_file` args are not a
special case to trim, they are working-set entries like any read — one live
copy per key in the whole assembly, wherever it lives.

## 3. Two-layer budget (item 1)

The budget has two layers (operator, 2026-07-16): what is **resident** in
context, and what is **tracked** — known to the ledger and re-addable on
demand. Eviction demotes content from resident to tracked; it never forgets
the key.

### 3a. Resident layer — what's IN context

- `wset_budget` = fraction of `ModelInfo.context_window` (default **25%**) —
  tiers 1 and 4 have their own budgets (skills catalog already 2%-class; recent
  window small by construction); tier 2 dialogue is uncapped until the
  last-resort summarization in §5, not a budget line.
- Token estimate: chars/4 (the meter's estimator; bounded error is fine for
  budgeting — provider `usage` via `Observe()` recalibrates a per-model
  correction factor).
- Over budget → demote **least-recently-touched** keys until under.
- **Hot set is immune**: keys touched in the last `hot_steps` (default 3) are
  never demoted regardless of size — never drop the file under active edit.
- **Hysteresis for prefix stability** (rule 5): demote episodically, not
  per-step — trigger at 100% of budget, demote down to **70%**. Between
  episodes the assembly is byte-stable and caching pays; a per-step trickle
  would churn the prefix continuously.
- Per-item cap first, then budget: any resident item > `max_retain_bytes`
  (default 64 KiB) is head-truncated with an idempotent marker (bridle/wset
  semantics) before it counts against the budget — one giant file or command
  dump cannot evict the rest of the working set (the 1.3M-token DeepSeek rep).

### 3b. Tracked layer — what can COME BACK

The thread JSONL is the cold store (persistence spec: full history, never
mutated) — so tracking costs only metadata. Per demoted key the ledger keeps:
key, thread seq of the last live copy, size, content hash, last-touch step,
staleness state. No second content store exists or is needed.

- **Demotion stub names the tracked state** (rule 3 — the stub sits in a tool
  result, where the model looks):
  `[working set: src/a.py demoted (untouched 12 steps, 3.1k tokens) — tracked; touch it or re-read to restore]`.
- **Re-admission** (tracked → resident) triggers, at the next assembly:
  1. the model **touches the key** — names it in any tool-call arg (read,
     edit, or otherwise). A fresh read re-admits trivially; for a NON-read
     touch (e.g. an edit) the harness re-admits the tracked copy itself, so
     the model is never editing a file whose content nothing in context holds;
  2. harness-initiated: a drift/diagnostic report or command output *mentions*
     the key (cheap string match over keyed paths) — anticipatory, optional
     (`readmit_on_mention`, default on).
- **Staleness gate**: re-admission serves the tracked copy ONLY if the content
  hash still matches disk (the §2 mtime-sweep / fs-watcher, mcp §5a); otherwise the key re-enters as
  `stale` — stubbed `[modified since last read: re-read for current content]`
  — and only a fresh read restores it. The tracked layer must never re-inject
  outdated truth; for artifacts with no disk ground-truth (web fetches, MCP
  results, unrepeatable command output) the tracked copy is always servable —
  there re-admission is a genuine recovery, not just a saved round trip.
- **Re-admission sources** (operator, 2026-07-16 — reentry after a harness
  read), by staleness state:
  1. tracked copy **valid** (hash matches disk) → un-stub the original bytes
     at the original position (free; re-aligns the pre-demotion prefix);
  2. **stale but disk-backed** → the HARNESS reads the disk itself and
     delivers fresh content appended to the *triggering* tool result (the
     edit result / drift report — the proven where-the-model-looks channel);
     no model step is burned and the ledger repoints the live copy. The stub
     never bounces work back to the model that the harness can do itself.
     Guard: harness reads only keyed paths inside the sandbox root;
  3. **no disk ground truth** (web fetch, MCP result, unrepeatable command
     output) → tracked copy served with a provenance marker — it is the only
     truth there is.
- **Partial re-admission** (operator, 2026-07-16 — the appropriate PARTS):
  the trigger carries a *locus* (edit range, symbol named in a diagnostic,
  error line); re-admit the span around the locus, not the whole file. Span
  resolution via an optional `SpanIndexer` seam — codemap's deterministic
  symbol index (exact line spans; the salvageable product of arm D) is the
  first implementation; LSP/tree-sitter slot in behind the same seam; no
  indexer → line-window fallback around the locus. Files < `partial_threshold`
  (4 KiB) always re-enter whole. Marker keeps it honest:
  `[partial: src/a.py L40–88 (decode_frame) — rest tracked; read_file for full content]`.
  One resident span per key in v2, widened when a touch lands outside it.
  Re-admission counts against the resident budget normally — it can trigger a
  demotion episode for colder keys.
- **Tracked-layer bound**: entries are ~100 bytes; cap at `tracked_max_keys`
  (default 1024, LRU) purely to bound sweep cost. Falling off the tracked
  layer loses nothing durable — the thread still has the bytes; the key just
  reverts to "re-read from disk" like any never-seen file.

The tiering that falls out: **hot** (resident, immune) → **warm** (resident,
LRU-eligible) → **tracked** (metadata only, instantly re-addable) → **thread**
(everything, forever). The model needs no awareness of any of it beyond the
stub text — rule 4, the harness does the bookkeeping.

## 4. Assistant-side curation mechanics

- Superseded `write_file` args: the tool_use block and id are preserved
  (strict-provider pairing, wset's structural rule); only the content arg is
  rewritten: `{"path":"a.py","content":"[superseded by later write/read; N chars]"}`.
  Happens once per supersede event (prefix-stable thereafter).
- **Reasoning replay** (thinking blocks / reasoning_content): owned by bridle's
  per-provider replay contract, NOT this algorithm — where the provider
  requires prior thinking blocks, they pass through untouched; where droppable,
  drop beyond the last `reasoning_keep_turns` (default 2). Instrument the
  share of the bill first (unmeasured as of this spec; flagged in testing as
  a suspect in DeepSeek's token-heavy reps).

## 5. Pressure & compaction episodes (seam mapping)

- `Observe(usage)` — updates the token-estimate correction and the pressure
  gauge (estimated next-assembly size vs effective window).
- Auto triggers, between sampling requests only (contract #5), in order:
  1. **LRU episode** (§3) — cheap, deterministic, usually sufficient (the
     arm F result is precisely that curation replaces compaction for
     tool-dominated threads).
  2. **Dialogue summarization** — LAST resort, only if still over window after
     the working set is at floor (hot set + recent window): summarize tier-2
     conversation older than the last `dialogue_keep_turns`, via a cheap local
     alias (`summarize` alias; Ornith-class). Fires Pre/PostCompact hooks and
     wire events; state fragments regenerate (contract #3).
- `Compact(manual)` = force sequence 1→2 now. `Compact(auto)` = the above.
- `context_length` error from bridle → one forced episode, retry once
  (contract #7).
- `/status` numbers come from the gauge (contract #8).

## 6. Config (profile-selectable, defaults from the testing)

```toml
[context]
# fs_watch = "notify"            # staleness watcher — DEFINED in agora-spec-mcp §5a (notify|sweep|off)
wset_budget          = 0.25      # resident layer, of context_window
keep_others          = 2         # recent non-keyed results, verbatim
max_retain_bytes     = 65536
hot_steps            = 3
evict_to             = 0.70      # hysteresis floor (demote-to)
tracked_max_keys     = 1024      # tracked-layer entry cap (metadata only)
readmit_on_mention   = true      # anticipatory re-admission (§3b trigger 2)
partial_threshold    = 4096      # files below this always re-enter whole
span_indexer         = "codemap" # optional; "" = line-window fallback
reasoning_keep_turns = 2         # where provider allows dropping
dialogue_keep_turns  = 8         # summarization threshold (last resort)
[context.keys]                   # tool → class/key-arg mapping
Read  = { class = "read",  key = ["file_path", "path"] }
Write = { class = "write", key = ["file_path", "path"] }
# legacy spellings stay mapped so pre-rename threads keep keying
read_file  = { class = "read",  key = ["path", "file_path"] }
write_file = { class = "write", key = ["path", "file_path"] }
apply_patch= { class = "edit",  key = "path" }   # invalidates, never carries truth
```

## 7. Contract compliance (context spec §2)

1. thread never mutated — algorithm is an Assemble-time projection ✓
2. Pre/PostCompact hooks — fired around summarization episodes (LRU episodes
   are not compaction: no information leaves the thread, only the view — they
   fire `thread.curation.demoted {keys, tokens_freed}` /
   `thread.curation.readmitted {key}` instead) ✓
3. state regenerated, never summarized ✓ (tier 1)
4. wire events — compaction pair as spec'd; plus the curation event above ✓
5. never mid-turn — episodes run between requests ✓
6. workflow journal / agent-graph untouched ✓
7. context_length → episode-and-retry ✓
8. /status ← pressure gauge ✓

## 8. Open items (v2, explicitly not blocking)

- **Cache measurement**: quantify `cached_tokens` under hysteresis vs sliding
  window on a caching backend (Anthropic now; Ornith when APC returns).
- **Bigger-than-budget working sets**: validate LRU behavior on a task whose
  working set genuinely exceeds the budget (the cwlog benchmark's fits; a
  100-file repo tour doesn't). The synthetic task exists to be built.
- **fs-watcher fidelity** for run_command invalidation — the watcher itself is
  spec'd in agora-spec-mcp §5a (notify primary, mtime-sweep fallback which
  over-invalidates in the safe direction; codemap's SweepChanged is the
  extracted pattern). Open item is only tightening run_command→path attribution.
- **Dialogue-side curation** ("latest statement of a recurring topic
  supersedes") — interesting, separate arc; composes with the fact store,
  doesn't replace it.
- Cross-thread persistence of the ledger (pods/handoff): the ledger is
  rebuildable from the thread by replay — persist nothing in v1.
