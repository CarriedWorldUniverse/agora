# ctxmap — a cross-referenced working memory for LLM harnesses

*Research write-up, updated 2026-07-11. Status: v0 built and validated on this
branch (`ctxmap-harness`); migration into the runtime harness is underway —
bridle PR #76 carries the system as `bridle/ctxmap/` packages, attached via
existing hook seams (zero bridle core changes). Agora remains the research
harness and evaluation bench. The dialogue results (§3) are strong and settled;
the agentic-coding arm (§3, "The agentic-coding test") is a closed, honest
negative result — memory rescues a *dialogue* context because a truncated fact
is gone, but a *coding* context is disk-recoverable, so eviction forces neither
a correctness failure memory can rescue (n=5) nor an efficiency cost memory can
recover (n=6); what coding wants is working-set retention, a different mechanism.
All numbers come from recorded runs — nothing is projected; small-n cells are
labelled as such.*

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

### The agentic-coding test: does memory carry a long tool loop?

The dialogue results above are strong where the map was designed to apply:
multi-turn conversations where a fact scrolls out between turns. The harder,
more valuable question is agentic coding — the operator's own diagnosis was
that most agentic-coding failure is *slow context degradation*: tool output
buries the structure (APIs, signatures, the spec) the model needs later. We
built a real experiment to test it, and it is the most instructive negative
result in the project.

**Setup.** `ctxagent` drives bridle's production tool loop over a sandbox
(read/write/run/list scoped to a temp dir), scoring by whether the task's test
suite passes. Two tasks were built: a 3-bug kv-store (too easy — every config
passed in ~4 steps, context never degraded) and then `cwlog`, a hardened
long-horizon task: an 11-file binary codec with 5 bugs whose *rules live only
in a SPEC.md*, checked against opaque golden fixtures so self-consistent-but-
wrong code passes its own round-trip but fails ground truth. Fixing it requires
recalling the spec across a long tool loop.

**First finding — a regime mismatch, found by instrumentation.** At full 256K
window every config passed `cwlog`, map on or off, at both 35B and frontier
scale. The map made no difference because it made no *appearance*: the engine
is a **between-turn** memory (assemble at turn start, extract at turn end), but
an agentic task is **one turn with dozens of internal tool steps**. A content
dump showed the injected block was assembled once, empty (`core=61 chars`), and
frozen for the whole loop. The map was architecturally inert inside the work it
was meant to help. This is why the dialogue wins did not transfer for free.

We built **within-turn mode** to close it: mine durable facts from tool results
as they stream, and keep a working-memory block in the (never-scrolled) system
prompt, refreshed only when it changes. It works mechanically — the block
populates from tool output — but surfaced the next constraint immediately: the
local extractor running *concurrently* with the loop starves it (12 threads on
a 16-core box drove steps from ~10s to ~130s). Between-turn extraction is free
because it runs after the turn; within-turn extraction competes. The in-harness
model needs its own compute, not shared cores — a hardware finding, not a design
flaw.

**Forcing the failure regime.** At full window nothing degrades, so we added an
eviction cap (`-keep N`): keep the last *N* tool results verbatim, replace older
ones with a stub. Structure is preserved (no API pairing errors) — only the
information is removed, which is exactly what a real long session does to early
tool output. The protocol was the operator's: **push map-off to reliable failure
first, then hold everything constant and intervene.**

We tested two memory conditions against no-memory: **seeded facts** (the 7 true
spec facts asserted as verified, no extractor — the *upper bound*, "if
extraction were perfect does the rest of the chain deliver?") and **seed +
working-state** (facts plus a second, deterministic memory — see below).

| model / memory | keep=2 | keep=4 |
|---|---|---|
| Ornith 35B, none | FAIL | FAIL ×2 |
| Ornith, seed facts | 1 PASS / 3 FAIL | 0 / 2 |
| Ornith, seed + working-state | 1 PASS / 1 FAIL | 0 / 3 |
| DeepSeek, none | 3 PASS / 2 FAIL (n=5) | 2 PASS (n=2)¹ |
| DeepSeek, seed + working-state | 4 PASS / 1 FAIL (n=5) | 2 PASS (n=2) |

¹ one of the two DeepSeek keep=4 no-memory passes cost **2.03M tokens** (a brute
re-read grind); the other 184k.

**We built the second memory the failures pointed at, and it did not help.** The
Ornith failure signature was consistent — it had the spec every step (confirmed
in block dumps) but lost its own *progress*: which files it had edited, what the
test last said. So we added **working state**: a deterministic "where am I" block
(files edited with counts, the last command's output, recent steps) built purely
from observing tool calls — no model, no extraction, always fresh. It rendered
correctly (it even surfaced a self-inflicted `ImportError` back to the model),
but the pass rate did not move: Ornith seed+state stayed ~1/5, keep=4 stayed 0/3.
A perfect *second* memory changed nothing. That falsified the "working state is
the missing half" hypothesis and pointed the diagnosis elsewhere.

**Then the capability test collapsed the rescue itself.** The obvious next
question — is the ceiling the memory or the model? — sent us to DeepSeek with the
best memory we had, under the same eviction. The first two reps looked decisive
(no-memory 0/2 → memory 2/2). They were noise: at n=5 it is **3/5 vs 4/5**.
There is no correctness rescue on this task. (This is why single-rep cells are
labelled throughout — Ornith and DeepSeek both swing wildly here, and a clean
2/2 that does not survive n=5 is exactly the trap.)

**Why it collapsed — the load-bearing finding.** The dialogue benches worked
because a truncated fact is *gone*: unrecoverable, so the store is the only path
to it. **Coding ground truth lives on disk.** Evicting tool *results* from the
context does not destroy the information — `SPEC.md` and the source files still
exist, and a capable model simply **re-reads them**. Eviction on a coding task
is not information loss; it is a re-read *tax*. A stable model pays the tax and
passes with or without memory; that is exactly what DeepSeek does (3–4/5 either
way), and the one keep=4 no-memory pass that cost 2.03M tokens *is* the tax made
visible. **The correctness-rescue thesis proven for dialogue does not transfer
to coding, for a structural reason: you cannot evict what the model can
reconstruct from disk.** The right metric for coding memory is therefore
**token-efficiency at matched success** (the cheapest passes were all
with-memory — 9–14 steps / 42–64k vs no-memory's 31–37 steps / 207–813k — but at
n=5 this is suggestive, not established), or a task whose lost information is
genuinely unrecoverable (a nondeterministic result, an external observation).
Pass/fail under disk-recoverable eviction measures the model's re-read stamina,
not its memory.

**And the local model fails for a different reason than information.** Ornith's
0/7 no-memory record is real, but the capability test shows *why* it is not the
"size doesn't substitute for the store" result the dialogue benches showed:
DeepSeek with the *same* degraded context mostly passes. Ornith fails from
**behavioral instability** — multi-minute thinking-crawls, 1–2M-token spirals,
premature quits — not from missing context (it had perfect context in the seeded
runs and still failed). For the sovereignty goal this is the sharp finding: the
blocker to a local <60B model doing real agentic work is the model's *stability
under load*, not the memory harness. A better-behaved small model plus this
memory is the path; more memory machinery is not.

**A note on the live extractor, kept for completeness.** Before the seeded runs
factored it out, the real 1.7B mining tool output produced a block of vacuous
facts ("uses the CRC32 checksum rule" — missing the load-bearing *payload-only*
detail), an unreconciled contradiction ("version is 2" and "version is 1" side
by side), and a confabulation ("the encode/decode functions are correct" — they
hold two bugs). The keel lesson reproduced *inside* the extractor. This is a real
weakness of the fact-extraction path — but the seeded upper bound shows fixing it
would not have changed the coding outcome, because the outcome was not
extraction-bound.

**The efficiency test — the metric the diagnosis says is the right one.** If
correctness is ill-posed under disk-recoverable eviction, the honest question is
cost: does memory make the model solve it *cheaper*? Three arms on DeepSeek, six
reps each, measuring steps and tokens among the *passing* runs (matched
success):

| arm | pass | median steps | median tokens | token range (passes) |
|---|---|---|---|---|
| A — full window, no memory | 6/6 | **7** | **~40k** | 33–59k |
| B — keep=2, no memory | 2/6 | 35.5 | ~383k | 343–423k |
| C — keep=2, seed + working-state | 4/6 | 30.5 | ~294k | 267–561k |

Two things, and the second is the answer. First, **eviction is catastrophic for
efficiency**: the full-window floor is 7 steps / 40k tokens; degrade the context
and the *same* PASS costs 4–5× the steps and 7–9× the tokens — the re-read tax,
measured. Second, **memory does not recover it.** With perfect seeded facts *and*
working-state, arm C is 30.5 steps / 294k — a ~15–25% shave on B, well inside the
noise (the passing-token ranges overlap; C has a 561k pass worse than any B
pass), and nowhere near A's 7 / 40k. Memory closes a small sliver of a large gap.

**Why memory can't close it — and what a coding memory would actually have to
be.** The dominant cost under eviction is not recalling the spec (injected,
cheap) or tracking progress (working-state handles it) — it is that with a
2-result window the model must **keep re-reading the source files to edit them**.
That live file content is not a durable fact and not session progress; it is the
*working set*, and a fact/state store neither holds it nor should. The thing that
would help is keeping (or cheaply re-serving) the files under active edit — a
smarter eviction policy that never drops the working set, which is "don't evict
what you're using", not "extract a memory from it". A fact store is a dialogue
instrument; it does not map onto the coding cost structure.

**Method note (an incident, kept not smoothed).** A flailing agent escaped the
sandbox through `run_command` — `read_file`/`write_file` were path-jailed but
`bash` was not — and overwrote the *pristine* task mid-campaign, silently
invalidating a repeat batch (caught by a file-mtime that matched a rep's end to
the second). `run_command` now runs in an unprivileged mount namespace with
`$HOME` read-only, every command is audit-logged, and a post-rep integrity gate
aborts the campaign if the task dir is ever dirtied. A benchmark harness must be
adversarial about its own ground truth; "the agent won't touch that" is not a
guarantee.

**The coding arm, closed.** Three results, each with a mechanism: no correctness
rescue (ground truth is disk-recoverable, n=5); no meaningful efficiency recovery
(~20% of a 7× tax, within noise, n=6); and the reason for both — the coding
working set is *live file content*, which a durable-fact/working-state memory
structurally does not hold. Memory's proven, valuable home is **dialogue**, where
a truncated fact is genuinely gone: there it rescues correctness, is
size-independent, and converts confabulation into honest abstention. Its coding
value is marginal, and what agentic coding actually wants — working-set retention
— is a different mechanism than the one this project built. That is the honest
end of the coding investigation.

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
10. **You cannot evict what the model can re-read.** The correctness-rescue
    result is real for dialogue because a truncated fact is *unrecoverable*.
    Coding ground truth persists on disk, so evicting tool results from context
    is a re-read tax, not information loss — and a capable model just pays it.
    A memory-rescue benchmark is only valid where the lost information cannot be
    reconstructed. For coding, measure token-efficiency at matched success, or
    make the lost information genuinely unrecoverable. We learned this the hard
    way: a clean 2/2-vs-0/2 rescue evaporated to 4/5-vs-3/5 at n=5.
11. **Memory does not fix a model that can't act on it.** Given a *perfect*
    working memory (seeded facts + deterministic working-state), a brittle 35B
    still failed ~4/5 under eviction — thinking-crawls, million-token spirals,
    early quits — while a capable model with the *same* context mostly passed.
    The bottleneck for local agentic work is model stability under load, not the
    memory harness. Two independent memory interventions (facts, then facts +
    working-state) both left the pass rate flat: the constraint was never the
    context.
12. **Token counters are one-sided; meter both ends.** A host's backend token
    count is remote-only — the local extractor/judge/distiller never pass
    through a provider, so their cost is invisible to it. Every "N× fewer
    tokens" claim is the *remote bill*; the sovereign-compute side is
    unmetered until you add it. Once metered: internal work is large but async
    and off the turn's critical path in between-turn mode, whereas the
    synchronous distiller was the entire map-on latency tax.
13. **Distilling code is the wrong tool.** Summarizing a file the model must
    edit byte-exact is lossy on the load-bearing bytes; measured ~5%
    compression for a full local-model call — pure latency for no saving.
    Distillation pays only on report-shaped output (logs, command results),
    never on source. Gate it by tool, not by size alone.
14. **A benchmark harness must guard its own ground truth.** An agent with a
    shell will, when lost, find and corrupt the pristine task if nothing stops
    it. Jail the tools, audit every command, and gate integrity after each rep
    — silent corruption produces invalid comparisons that look like data.

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

The agentic-coding arm (§3) is **closed** — both the correctness and the
efficiency questions were run to conclusion, both negative with a mechanism. The
directions it leaves are not "finish the coding memory"; they are the pointers it
produced:

1. **If coding memory is ever revisited, it must retain the working set, not
   extract from it.** The efficiency test showed the dominant eviction cost is
   re-reading live file content — not facts, not progress. The lever is a
   working-set-aware eviction/retention policy (never drop the file under active
   edit), which is a harness scheduling feature, not a memory store. The fact
   store does not map onto the coding cost structure and should not be forced
   onto it.
2. **Model stability, not memory, is the local-agentic blocker.** A perfect
   memory did not rescue Ornith (thinking-crawls, token spirals, early quits)
   where the same context carried DeepSeek. The sovereignty path runs through
   a better-behaved small model — or guardrails on the pathology (thinking-
   budget caps, loop detection) — not more memory machinery. This is the real
   reframing of the local-model effort, and it is independent of ctxmap.
3. **The correctness-rescue claim stays scoped to dialogue**, where the lost
   fact is genuinely unrecoverable and the result is proven. Testing it in an
   agentic setting would need a task whose lost information cannot be re-read (a
   nondeterministic result, an external observation) — a different task, not a
   different memory.

Both component memories survive the close: the working-state block is cheap,
deterministic, correct instrumentation (useful for any agentic harness,
independent of the rescue question), and the fact store is the proven dialogue
win. Neither is invalidated — only the coding *rescue* framing was, and it is now
retired.

Migration target: the runtime harness (bridle). The store/renderer/extractor
trio lifts out — the research turn loop here was always scaffolding (it is
itself a copy of bridle's). Open decisions: module boundary (separate ctxmap
module vs in-tree), the cgo/model-weight footprint (likely build-tag gated),
store scope in a multi-agent fleet (lean: per-repo), and whether ctxmap
becomes the funnel's missing session persistence. Rollout: hooks-only PR,
then flag-gated wiring, then one aspect soaked against this repo's bench
before anything fleet-wide.
