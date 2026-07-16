# agora spec — workflows (starlark)

Decision (operator, 2026-07-15): workflow scripts are **starlark**. This is agora's differentiator — neither codex nor Claude Code exposes user-constructible orchestration as a first-class, saved, resumable artifact. Design synthesized from observed Claude Code Workflow-tool behavior + agora requirements. Runs on the subagent primitive (agora-spec-subagents.md).

## 0. Why starlark is the right fit

- **Hermetic & deterministic by construction**: go.starlark.net has no ambient IO, no clock, no randomness unless the host injects them — the determinism rules a journaled-resume system needs are enforced by the interpreter, not by convention.
- Real control flow (loops, conditionals, functions) — workflows are *constructed*, not just declared DAGs.
- Already in the ecosystem: codex's execpolicy is a starlark DSL; if agora ports execpolicy it shares one embedded language.
- Python-ish surface — models write it fluently.

## 1. Artifact shape

A workflow = one `.star` file in `<project>/.agora/workflows/` or `~/.agora/workflows/`, invocable by name (TUI `/workflow <name> [args]`, headless `agora workflow run <name> --args '<json>'`, or inline from a turn).

```python
meta = workflow_meta(
    name = "review-changes",
    description = "Review changed files across dimensions, verify each finding",
    phases = [                              # progress groups for the UI + per-phase defaults
        {"title": "Review", "model": "local-fast", "effort": "low"},
        {"title": "Verify", "model": "frontier",   "effort": "high"},
    ],
    args_schema = {...},                    # optional JSON schema for args validation
)

def main(ctx, args):
    findings = ctx.parallel([
        lambda: ctx.agent(d["prompt"], label = "review:" + d["key"],
                          phase = "Review", schema = FINDINGS_SCHEMA)
        for d in DIMENSIONS
    ])
    verified = ctx.pipeline(flatten(findings),
        lambda f, _, i: ctx.agent("Adversarially verify: " + f["title"],
                                  phase = "Verify", schema = VERDICT_SCHEMA))
    return {"confirmed": [v for v in verified if v and v["is_real"]]}
```

(Exact idiom TBD in implementation — `ctx` receiver keeps the host API in one namespace and starlark-friendly. `main(ctx, args)` is the entry point.)

## 2. Host API (the `ctx` module)

- `ctx.agent(prompt, label=?, phase=?, agent_type=?, model=?, effort=?, schema=?, isolation=?) → value`
  Spawns a subagent, blocks the starlark green-thread until done. Without `schema`: returns final text. With `schema` (JSON schema as a starlark dict): child is forced through a StructuredOutput tool, result validated (retry on mismatch), returns the decoded value. Returns `None` if the agent dies/skipped — callers filter.

  **Every `ctx.agent()` IS a real subagent** (agora-spec-subagents.md): a child funnel session with its own context window, resolvable `agent_type` (the same `.agora/agents/*.md` defs — a workflow stage can be "run the `reviewer` agent"), an edge in the agent graph (so the TUI's tree and `workflow watch` come for free), and its transcript in the thread store. The workflow engine is orchestration ONLY — it owns no execution machinery of its own.

## 2a. Per-stage model & effort routing (first-class)

Different stages need different horsepower; the workflow is where that gets encoded.

- **Resolution order** for each `ctx.agent()` call: explicit `model=`/`effort=` on the call → the phase's defaults from `meta.phases` (calls tagged `phase="Verify"` inherit that phase's `model`/`effort`) → the `agent_type` def's frontmatter → inherit the parent session's model/effort. Same order for both knobs, resolved independently.
- **Model aliases, not raw ids**: scripts name tiers (`"local-fast"`, `"local-heavy"`, `"frontier"`, `"cheap"`), resolved through **bridle's model registry** at run time (e.g. local-fast → GB10 qwen on robo-dog, frontier → claude via bridle). Raw provider ids are accepted but aliases keep workflows portable across providers and hardware — the same review workflow runs all-local or frontier-verified by changing the registry, not the script. Alias table lives in agora config; unresolvable alias = error at run start, not mid-run.
- **Effort tiers**: `low | medium | high | xhigh | max`, passed through bridle to whatever the provider supports. **Default `high`** (operator preference: xhigh's token cost isn't worth the marginal gain — see [[feedback-effort-prefer-high]]); `xhigh`/`max` are opt-in for explicitly correctness-critical stages, not a default.
- **Guidance (encode in the workflow-creator skill)**: mechanical/extraction/fan-out stages → cheap local model, low effort; synthesis and adversarial verify/judge stages → strongest model, high effort; when unsure, omit and inherit. The budget (`ctx.budget`) composes with this — a run can afford far more cheap-stage agents than frontier ones, and `spent()` counts real per-model cost, not just tokens, once bridle exposes pricing.
- Journal note: model/effort resolve into the journal entry's opts hash — editing a stage's model correctly invalidates that call's cached result on resume, while other stages replay.
- `ctx.parallel(thunks) → list` — run concurrently, **barrier**: await all. A failed thunk yields `None` in the result list; the call never aborts the sibling thunks.
- `ctx.pipeline(items, *stages) → list` — each item flows through all stages independently, **no barrier between stages** (item A in stage 3 while item B in stage 1). Stage signature `(prev, original_item, index)`. A stage error drops that item to `None` and skips its remaining stages. **Default to pipeline; use parallel only when a stage genuinely needs all prior results (dedup/merge/early-exit).**
- `ctx.phase(title)` / `phase=` opt — progress grouping for the UI (per-call opt avoids races inside pipelines).
- `ctx.log(msg)` — narrator line to the user's progress view.
- `ctx.workflow(name, args) → value` — invoke a saved workflow inline; shares concurrency cap, agent counter, budget; **one level of nesting only**.
- `ctx.budget` — `{total, spent(), remaining()}` token budget for the run; when a total is set it's a hard ceiling (agent() raises once exhausted). Enables `while ctx.budget.remaining() > 50000:` scaling loops.
- `ctx.question(payload) → answer` — raise a `question`-kind card up the escalation ladder (agora-spec-planning-questions §4/§5). **Blocking by construction** (starlark needs the return value): the RUN parks (`waiting-on-answer` run status — runs are background objects already; no thread is held). The answer **journals into the entry hash** — an answer is an input like `ctx.args`, so resume replays answered stages deterministically; a daemon restart mid-question replays to the unanswered call and re-raises it. Questions raised by agents *inside* a stage bubble to the engine first — it may answer from values the script already holds (a prior stage's result), else the run parks. Fire-and-continue questions belong to an agent calling the `question` tool with `blocking:false`, not to this call.
- `ctx.approval(msg, payload=?) → bool` — `gate`-kind approval (approvals §1): allow/deny + message, journaled identically. Same pipeline as `ctx.question` — **one implementation, two verbs** (workflow-engine v1, not v1.1: by build order the approvals/question machinery exists before the engine does).
- Injected constants: `ctx.args` (validated), `ctx.now` (run-start timestamp, frozen — the only clock).

Deliberately absent (enforced by starlark itself): wall clock, randomness, filesystem, network. Anything an agent can do, do *in an agent*.

## 3. Execution engine (Go)

- Each `ctx.agent`/thunk runs the blocking starlark call on its own goroutine with its own `starlark.Thread`; the scheduler enforces the global concurrency cap (min(16, cores-2)), queuing excess. Lifetime cap per run (e.g. 1000 agents) as a runaway backstop; per-call item cap (e.g. 4096).
- Cancellation: run-scoped context; user pause/kill cancels in-flight children cleanly.
- Live progress: phases → groups, each agent a row (label, status, tokens); TUI renders from the agent-graph + run events. `agora workflow ps / watch` for headless.

## 4. Journal & resume (the property that makes workflows constructible)

- Every `ctx.agent()` call appends `{seq, prompt_hash(prompt+opts), result}` to `journal.jsonl` in the run dir; `ctx.log`/phase events too. `ctx.question`/`ctx.approval` append `{seq, payload_hash, answer|decision, by}` the same way — answered stages replay from the journal on resume.
- **Resume**: re-run with `--resume <run_id>` after edit/pause/crash → replay the journal: the longest prefix of agent calls whose (prompt, opts) hash matches returns cached results instantly; first mismatch and everything after runs live. Same script + args ⇒ 100% cache. This is why determinism matters and why the clock is frozen: iteration on a 50-agent workflow costs only the edited tail.
- Run dir: `~/.agora/workflow-runs/<run_id>/` — script snapshot, args, journal, per-agent transcript refs, final result.

## 5. Invocation & construction UX

- `/workflow <name>` in TUI; `agora workflow run|ps|watch|resume|list`.
- **Construction is conversational**: the main agent authors/edits the `.star` file like any file, then invokes it — same loop as "edit skill, use skill." Ship a `workflow-creator` skill (like codex's skill-creator) encoding the patterns below.
- Args are real JSON values (validated against `args_schema`), not stringified blobs.

## 6. Pattern library (document in the workflow-creator skill, not the engine)

- **Adversarial verify**: N independent skeptics per finding, each prompted to refute; kill on majority refute.
- **Perspective-diverse verify**: distinct lenses (correctness/security/repro) instead of N identical refuters.
- **Judge panel**: N independent attempts from different angles, parallel judges score, synthesize from winner.
- **Loop-until-dry**: for unknown-size discovery, keep spawning finders until K consecutive rounds add nothing new (dedup against all-seen, not just accepted).
- **Multi-modal sweep**: parallel agents each searching a different way.
- **Completeness critic**: final agent asks "what's missing?" — its answer seeds the next round.
- **No silent caps**: if a workflow bounds coverage (top-N, sampling), `ctx.log` what was dropped.

Maps directly onto the existing ticket-pipeline: intake → fan-out builders → reviewer → security-validator → PR becomes a saved workflow with human-approval gates at phase boundaries (`ctx.approval` at phase boundaries, `ctx.question` for intake clarification — both v1, §2).

## 7. Sizing

v1 = meta + agent/parallel/pipeline/log/phase + **per-stage model/effort routing (§2a)** + **ctx.question/ctx.approval (§2 — one pipeline implementation)** + journal/resume + named invocation + TUI progress. Defer: budget directives, nested workflow(), durable parked-run state (v1 recovers a parked run by journal-replay-and-re-ask; surviving restart as a live "waiting" object is the refinement), remote/cron triggering (nexus dispatch integration — later these become dispatchable units on the broker).
