# ctxmap — a cross-referenced working memory for LLM harnesses

*Research write-up, 2026-07-09. Status: v0 built and validated on this branch
(`ctxmap-harness`); migration into the runtime harness is underway — bridle
PR #76 carries the system as `bridle/ctxmap/` packages, attached via existing
hook seams (zero bridle core changes). Agora remains the research harness and
evaluation bench. All numbers below come from recorded runs in `bench.db` and
the frozen golden-set scorer — nothing is projected.*

## 1. The idea

A conventional LLM harness gives the model one kind of memory: the transcript.
Every turn replays as much history as fits, and the model's attention does the
cross-referencing — implicitly, transiently, and degradingly as context grows.
When the window fills, compaction or truncation discards precisely the thing
that made the history useful: the *joins* between facts. Conclusions reached
mid-session live only in the token stream; stale facts and their corrections
coexist and the model attends to whichever is closer; and a fact dropped from
the window is simply gone.

ctxmap makes the cross-referencing **explicit and durable**. A small CPU model
watches the conversation and distills each turn into *facts* — short,
self-contained declarative statements with provenance, a kind, a source, and a
trust rank. Facts live in a store where they link, contradict, get corrected,
and get promoted. Each turn, the harness assembles a prompt from a small
stable **core** of verified facts, a per-turn **subgraph** of relevant facts,
and a short raw tail — instead of the whole transcript. The big model also
gets a `recall` tool to pull older facts on demand.

Two principles anchor the design:

- **The transcript is ground truth; the map is a cache of interpretations.**
  Every fact carries a provenance span pointing back into the transcript. The
  store can be deleted and rebuilt; losing it loses convenience, never truth.
- **Model say-so never mints truth** (the lesson of a prior incident where an
  agent's self-reported success was trusted). A fact cannot be constructed
  without provenance; extractor confidence never promotes a fact; operator
  trust is granted only when the statement is deterministically grounded in
  the operator's own words.

## 2. Architecture as built

```
turn ends → EXTRACTOR (Qwen3-1.7B, in-process llama.cpp, async)
              → FactProposals {statement, kind, source, entities, confidence}
            → 4B JUDGMENT PASS (Qwen3-4B, thinking): kind + source per fact
            → RECONCILER: embedding-gated candidates (nomic-embed) →
              token-identity fast path → 4B pair judgment
              SAME / CONTRADICTS / DISTINCT
            → STORE (SQLite): provenance-mandatory facts, links, lifecycle
prompt    ← RENDERER: [system + memory-framing + epoch-frozen core]
              [raw tail (small)] [per-turn subgraph] [user msg]
            + recall/inspect tools served to the model
```

**Fact lifecycle.** Facts enter `PROPOSED`, except operator-stated
*performatives* — decisions, rules, namings, stated intents, where saying
makes it so — which enter `VERIFIED` (grounded in the user's words, and
force-classified: operator *reports* of world state enter PROPOSED, and
question presuppositions are dropped — see §5). Promotion paths:
operator pin, or reuse-confirmation — rendered into context in ≥3 distinct
turns without acquiring a contradiction. Trust ranks
`OPERATOR_STATED > MODEL_OBSERVED > MODEL_DERIVED`; a newer fact from a
strictly-higher trust source auto-retracts what it contradicts (an operator
correction always wins); equal-trust model-vs-model contradictions are flagged
for both to see, never silently resolved. Retracting a parent flags derived
descendants stale. Only `VERIFIED` facts enter the core block. A deny-pattern
pass at the store boundary rejects credential-shaped content.

**Cross-session rule.** One store per project; `VERIFIED` facts are
project-wide, `PROPOSED` facts are visible only to the session that asserted
them. A new session inherits the project's verified knowledge and none of any
prior session's speculation.

**Prompt layout and caching.** All per-turn churn (the subgraph) sits at the
END of the prompt; the core block is byte-stable within an epoch. Cache
stability is a *within-turn* property — the tool loop's steps must append
monotonically — while between operator turns a reseed is cheap (human
think-time dwarfs it), so the harness auto-consolidates (re-renders the core)
at any turn boundary where the verified set moved, and does not replay old
subgraph injections in the tail.

**Evaluation surfaces.** A control MCP server (`ctxmapd`) exposes
`session_start` (with a map-on/off ablation switch and a `project` param),
`prompt` (returns the answer plus the map's side effects), `map_query`/
`map_inspect`/`map_stats`, `render_preview`, and `store_audit` — the harness
is evaluated by driving the harness. A bench runner (`ctxbench`) replays
versioned scripted workloads with machine-checkable probes, records
fingerprinted rows (harness rev + model digests + prompt hashes + config —
dirty trees get a `-dirty` suffix) with per-turn token/cache/latency stats
into `bench.db`, and compares pass rates across exactly-one-dimension diffs.

## 3. Headline results

### The thesis test: joins the context can no longer hold

Overflow workloads pad turns with realistic bulk until the session provably
exceeds the model's 256K window; facts planted early are truncated out of
every harness's visible context; probes require *joining* two planted facts,
with parallel decoy chains so the right answer cannot be guessed from what
remains visible (a probe a guesser passes measures nothing — our first
version failed this and was redesigned).

| harness | multihop joins | longhaul-v2 joins |
|---|---|---|
| ctxmap, tail=2 | **100%** (3 reps) | **100%** (2 reps, 4/4 probes) |
| standard (200k budget, truncating) | 50% = guess rate | 75% ≈ guess rate |

The standard harness's failures are **confident confabulations** — it invented
a "semantic naming convention" rationale for the wrong answer, twice, in
opposite directions. ctxmap's sole failure mode observed (an extraction gap,
since fixed) was an honest *"I don't know — working memory has no fact about
that"* after three recall attempts. A memory harness changes not just the
accuracy but the *character* of failure.

### Cross-model: does model size substitute for the store?

The same memory test (three parallel invented-token chains — no world knowledge
can help, guess floor ~1/3 per probe — planted early and truncated out of a
2-turn tail) run across a 10× size range, map-on vs map-off, 9 probe-passes/cell:

| model | map-on | map-off |
|---|---|---|
| Ornith ~35B | 67% | **0%** |
| GLM-4.6 ~355B | 56% | **0%** |
| DeepSeek-V4 | **100%** | 56%¹ |

Two findings, one of them the most important result in this document:

1. **Size does not substitute for the store.** Without memory, a 355B model
   confabulates a fact from six turns ago exactly as confidently as a 35B one —
   both invented profiler names on *every* map-off probe (Quill, Prophet, Atlas,
   Torch, wayfarer…). Not one of the three models, at any scale, said "I don't
   know" without the store. You cannot attend to what isn't in the window, and
   more parameters don't change that.
2. **The store's deepest value is honesty, not just accuracy.** GLM's one map-on
   "miss" was it stating *"working memory records this routes to aurora-queue,
   but not which profiler — I don't have that"* — an honest abstention citing
   the store, where the *same model* invented a name every time map-off. The map
   converts confident confabulation into grounded uncertainty. For agentic
   reliability that matters more than the accuracy delta.

Caveats, recorded not smoothed: map-on is not a clean 100% for the smaller
models because it now rides on **extraction reliability** (whether the local
Qwen hybrid captured the fact) plus recall-tool use — not the big model;
DeepSeek hit 100% by using recall best. This is the binding constraint the
project already knew, and exactly what a trained in-harness model would target.
¹ DeepSeek's map-off passed the first probe 3/3 — "glimmer" is likely the most
guessable invented name; randomizing name↔chain would remove the residual. n=3.

### Token economics: what the model reads at the moment of answering

On the bulk-in-history workload (18 padded turns ≈ 324k tokens of history,
short probe questions):

| | probe-turn prompt | session total | wall/rep |
|---|---|---|---|
| ctxmap, tail=2 | **1,577–3,678 tokens** | ~1.15M | ~60 min |
| standard, 200k budget | 144,071–151,232 tokens | ~2.77M | ~72 min |

**~40–90× fewer tokens at the moment of answering, at better accuracy** — on
a backend with prefix caching disabled, the regime least favorable to us.
The corollary the numbers force: with a working store, the tail can be tiny
(tail=2 dominated every configuration tested on quality, tokens, and
latency simultaneously), because the store — not the context window — is the
memory. This also matches the operating experience that motivated the
project: models perform best when *not* swamped; curation beats capacity.

### Extraction quality (frozen golden-set scorer, 25 labeled cases)

Winning config: Qwen3-1.7B extracts, Qwen3-4B judges kind+source per fact
(thinking enabled). Current shipped prompt (v3.2):
precision 74–79 / recall 84–87 / kind 65–73 / entity 95 / source 88–92
across recent runs. Gemma 4's mobile class (E2B extract + E4B judge) was
benched as the planned alternative and rejected: P 55 / R 71 / entity 68 and
2× slower on CPU (its effective-size architecture computes near full size).
The caveat is recorded — our prompts are Qwen-tuned and prompt-model coupling
is real — but the 23-point gap exceeds any tuning delta ever observed here.

## 4. Findings (the transferable ones)

1. **Embeddings detect topic, not truth.** Calibration on labeled pairs:
   duplicates score 0.88–0.94 cosine, contradictions 0.86–1.00 ("caps at 40"
   vs "caps at 12" = 0.996), unrelated ≤0.65. A contradiction IS a
   near-paraphrase with one changed value. Any memory system that dedupes on
   embedding similarity alone will silently merge corrections into the facts
   they correct. ctxmap uses cosine only to gate same-topic candidates; a
   judgment model decides SAME/CONTRADICTS/DISTINCT.
2. **Small model proposes, judgment model disposes.** The 1.7B extracts as
   well as the 4B (better, in fact) but cannot make judgment calls: kind
   classification (52% → 74%), source attribution (44% → 92%), and pair
   relations all needed the 4B thinking pass. Three separate instances of the
   same division of labor.
3. **Grounding gates beat trust.** The extractor *proposes* that the user
   asserted a fact; operator-level trust is granted only if ≥60% of the
   statement's content words appear in the user's text. Model attribution
   errors are conservative (missed grants), never unsafe (false grants need
   two independent failures).
4. **Prompts are model-coupled.** The 1.7B-tuned extraction prompt lost 19
   precision points when run on the 4B; few-shot examples at 1.7B transferred
   *density* rather than *distinctions* and made things worse. Model digest
   and prompt hash must fingerprint together.
5. **Prompt rules see-saw at small scale.** Fixing parallel-decision fusion
   cost chit-chat exclusion (~4.5 precision points). We shipped the trade
   deliberately: missed operator decisions are unrecoverable; junk facts are
   bounded by PROPOSED status and the reconciler. The golden set tracks both
   failure modes for future extractor candidates.
6. **Cache stability is a within-turn property.** Tool-loop steps must append
   monotonically (a mid-prompt churn block cost a full cold prefill *per
   step*: 15+ min/turn until fixed). Between operator turns, reseeds are
   cheap — which unlocks auto-consolidation and eliminates replaying injected
   blocks. And prefix caching fixes latency, never tokens sent: payload
   economics come from the small tail, not the warm cache.
7. **The model must be told memory is automatic.** Given read-only memory
   tools and the word "remember", the model refused to "save" facts and
   burned tool calls apologizing. One framing block ("memory is captured for
   you automatically; just converse") fixed the behavior completely.
8. **Benches must force the failure regime.** Two workloads in a row passed
   for the wrong reason (a guessable probe; a too-shallow history that let
   the standard harness legitimately keep the plant). Decoy chains and
   provable overflow are now workload requirements, and per-turn
   instrumentation is what caught the second error.
9. **Running the system beats reviewing it.** Fifteen-plus defects — a
   process-killing GGML assert, a 2-hour stall on a lost response with no
   client timeout, a cache-layout violation of our own spec, fingerprint
   contamination from dirty trees — were all found by the bench or by
   driving the harness through its own MCP, not by inspection.

## 5. Trust, deference, and the challenge principle

The trust model separates two things that are usually conflated:

**Epistemic trust** — whose account of the world wins when accounts conflict.
The operator outranks the model, always: they have access to reality the model
doesn't. This is the trust ladder, refined by utterance force: operator
*decisions* are performative (saying makes them so — VERIFIED on entry);
operator *reports* of world state can be honestly mistaken (top conflict rank,
but PROPOSED entry); *questions* assert nothing, and the presuppositions
inside them are dropped rather than minted into facts.

**Deliberative deference** — whether the operator's *wanting* something ends
the conversation. It doesn't, and the data model says so deliberately: a
directive is stored as "the operator wants X", which is unfalsifiably true,
*and stops there*. The store never records "X is wise" or "X will work". The
gap between want and wisdom is where the assistant's judgment is supposed to
live — in the dialogue, not the database.

The intended disposition for an agent running this memory — **the challenge
principle**: the standard is the operator's *outcome*, not the operator's
instruction.

- **Challenge with evidence, not vibes.** A challenge must name the specific
  conflict — a constraint, a measurement, a prior operator statement. If the
  agent can't name one, it executes and lets reality report back.
- **Challenge once, then commit.** Raise it, take the answer, execute
  wholeheartedly. No relitigating, no "as I mentioned".
- **Scale to stakes.** Irreversible, expensive, or contradicting the
  operator's own recorded goals → always worth the interruption. Taste and
  style → never; the operator's preference *is* the outcome there.
- **The strongest challenge is a better alternative**, not an objection.

This is what the intent/constraint distinction is *for*: because "operator
wants X" and "constraint: never Y" are both first-class facts, the store makes
their conflicts **detectable** — and a detected conflict between the
operator's ask and the operator's own verified constraints is precisely the
evidence a justified challenge requires. An agent with this memory can say
"you asked for X, but X contradicts the constraint you verified last month" —
a categorically better challenge than anything a transcript-window agent can
mount. Cheap challenge, expensive silence; but only ever with evidence.

## 5a. Running it

```
# build (llama.cpp libs staged once)
make vendor-llama && go build ./...

# control MCP (stdio) — evaluate the harness by using it
go build -o ctxmapd-bin ./cmd/ctxmapd && ./ctxmapd-bin
# env: CTXMAP_BASE_URL (OpenAI-compatible backend), CTXMAP_EXTRACT_MODEL,
#      CTXMAP_KIND_MODEL, CTXMAP_EMBED_MODEL, CTXMAP_DATA_DIR

# bench a workload (fingerprinted rows into bench.db)
go build -o ctxbench-bin ./cmd/ctxbench
./ctxbench-bin -workload workloads/correction-fits.json -map=true -tail 2 -reps 3

# extraction golden set (frozen scorer) lives in the sibling research dir
```

Models (all CPU, in-process): Qwen3-1.7B-Q8 (extract), Qwen3-4B-Q8 (judge),
nomic-embed-text-v1.5-Q8 (topic gating). ~11GB resident total. The backend is
any OpenAI-compatible endpoint; all validation ran against a local model
(Ornith via litellm), so round trips were free.

## 6. State and road map

Done: everything in §2–§3, on this branch, with tests. Watch items: the
extraction junk class (bounded by lifecycle + reconciler; a stronger small
model likely clears the see-saw), reconciler extraction cost on junk-heavy
turns (never blocks a turn; visible in bench waits), statistical weight on
the overflow results (n=2–3 per cell), and prefix caching's return on the
local backend (cache-hit% instrumentation is already wired and honest).

Migration target: the runtime harness (bridle). The store/renderer/extractor
trio lifts out — the research turn loop here was always scaffolding (it is
itself a copy of bridle's). Open decisions: module boundary (separate ctxmap
module vs in-tree), the cgo/model-weight footprint (likely build-tag gated),
store scope in a multi-agent fleet (lean: per-repo), and whether ctxmap
becomes the funnel's missing session persistence. Rollout: hooks-only PR,
then flag-gated wiring, then one aspect soaked against this repo's bench
before anything fleet-wide.
