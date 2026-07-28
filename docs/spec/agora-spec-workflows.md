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

## 8. In-session invocation & live progress (spec addition, 2026-07-27)

§5 already names `/workflow <name>` and §3 already names "TUI renders from the
agent-graph + run events". Neither is built: `agora workflow run|list|resume`
exists, `ps`/`watch` do not, no slash command exists, and
`contracts.ItemWorkflowProgress` is defined and registered in the contracts
known-items test with **zero emitters**. This section specifies the delta,
because the one-line §5 is not enough to implement against and the gaps are
where the sharp decisions live.

### 8.1 The invocation surface is OPERATOR-invoked, not model-callable

`/workflow <name> [json-args]` in the TUI; `agora workflow run` headless. There
is deliberately **no `workflow` tool on the model's surface**.

A run may spawn up to the lifetime agent cap (1000). The operator typing the
command IS the authorization for that, and it is a far better one than an
approval prompt on a tool call whose blast radius the prompt cannot convey. A
model-callable `workflow` tool would need its own approval kind, a cost
pre-estimate the budget stub (§7 deferred) cannot yet produce, and a policy
answer for `never-escalate` — three unsolved problems to buy a capability the
operator can already trigger in one keystroke.

Rejected alternative, recorded because it was proposed: exposing `workflow` as
a native tool alongside `agent`. If it is ever wanted, the prerequisites are
(a) a real `ctx.budget` so a pre-flight cost estimate exists, (b) a distinct
`ApprovalKind` — reusing `KindExec` would inherit auto-run under `auto-safe`
and `never-escalate`, which is the wrong default for something that can spawn
a thousand children, and (c) a decision on whether a *subagent* may start a
workflow (default: no — `enginerunner` already never re-wires `agent()` onto
children as a structural depth guard; the same guard should cover workflows).

### 8.2 A run is a background object. Nothing about it may block a turn

§2's `ctx.question` note already establishes this — "the RUN parks
(`waiting-on-answer` run status — runs are background objects already; no
thread is held)". Make it a hard invariant for the in-session lane:

> **Invariant W1.** `/workflow` starts a run and returns immediately. No
> workflow operation is ever a blocking tool call in the parent turn.

This is agora#152 made structural. That incident was a *foreground* `agent()`
spawn blocking its parent's tool call with no timeout and no visible signal,
for 30+ minutes. A workflow is strictly longer-running than a single subagent,
so the same shape would be strictly worse. The run's lifetime is decoupled
from the turn that started it: the turn ends, the run continues, progress
keeps arriving on the session's event stream.

### 8.3 Hosting: through the shared engine seam, not a second lane

`agora workflow run` today builds its own invoker
(`cmd/agora/workflow_agent.go`'s `buildWorkflowInvoker`) with
`enginerunner.New(provider, store, WithProfile(prof))` — bypassing
`newTurnEngineManager`. That is a fourth engine-construction lane and it drifts
from the three the README describes: hooks (`DiscoverHooks`), `.mcp.json`
servers (`buildMCPSource`), the operator's `default_effort`, and the durable
permissions store never reach a workflow-spawned agent.

An in-session run MUST be hosted through the same shared seam as the TUI, pipe
and daemon lanes, so a workflow stage sees the same tool surface, hooks and
grants the operator's own turns do. Consolidating the headless
`agora workflow run` path onto that seam is in scope; leaving two lanes is not.

### 8.4 Thread topology and the agent graph

The run gets its own thread, registered as a **child edge of the session
thread** in the agent graph. §2 already relies on this ("an edge in the agent
graph — so the TUI's tree and `workflow watch` come for free"), and
`RegisterRoot` already exists for the headless case.

Consequence worth stating: because `EdgeClosed` means *"hides the subtree"*
rather than *"finished"* (see `graph.go` — a completed child stays
resumable-by-continuation and its edge stays open), the graph answers "what is
the shape of this run" and NOT "what is running right now". Live state comes
from run events, not from edge status. A display that infers "running" from an
open edge will report every workflow the thread ever started as live.

### 8.5 Progress events: wire `ItemWorkflowProgress`

Reuse the item-event machinery, exactly as `agent_spawn` now does
(agora#155/#156) — one mechanism, not a parallel notification channel.

- `item.started` `workflow_progress` when the run starts:
  `{run_id, name, phase_count, args_summary}`
- `item.started`/`item.completed` pairs per **phase**, keyed by the phase title
  so a phase is a group in the display:
  `{run_id, phase, agents_started, agents_done}`
- `ctx.log` narrator lines emit a progress item carrying `{run_id, message}`
  rather than printing anywhere directly — headless and TUI then render the
  same stream.
- `item.completed` `workflow_progress` on run end:
  `{run_id, status, agents_total, elapsed, error?}` with
  `status ∈ running|parked|completed|failed|killed`.

Per-agent rows come from the `agent_spawn` items each `ctx.agent()` already
produces via the subagent path — the workflow layer does not re-emit them.

### 8.6 Rendering

- **Status row**, while any run is live: `· workflow <name> [phase 2/5] 3 agents`
  — the same pattern as the subagent segment added in agora#156, and for the
  same reason: scrollback is not a live view.
- **Transcript**: phase transitions and `ctx.log` lines, so the arc is
  readable after the fact.
- **A parked run MUST be visibly parked.** `waiting-on-answer` in the status
  row plus the question card. A parked run that looks identical to a busy run
  recreates agora#152 exactly — an operator waiting on something that is
  waiting on them.
- `agora workflow ps|watch` render the same events headless (§5's unbuilt half).

### 8.7 Questions and approvals from a run

These already work, and the mechanism is what makes the in-session lane
tractable: `QuestionRouter.Ask` goes through `planning.QuestionLog` with
`Blocking: true`, and answers are resolved by `lookupAnswer` reading persisted
thread items. Questions are **store-mediated, not channel-mediated**, so any
client attached to the run's thread can answer one — no new transport.

Requirement: the session's TUI must be attached to the run's thread (§8.4) for
its cards to be answerable, and §8.6's parked indicator is not optional.

### 8.8 Approval policy for run-spawned agents

The `agent()` lane gives children `PresetNeverEscalate` (agora#152/#153),
because a subagent has no approver and a policy that can ASK parks the child
and its parent forever.

A workflow run is **not** in that position: it has an operator, reachable via
the store-mediated path above. So run-spawned agents may keep never-escalate
for their *own* tool calls (unattended stages should not stall), while the
*script* escalates deliberately through `ctx.approval` at phase boundaries —
which is exactly the split §2 already describes ("workflow gates always
surface to the operator"). Stated as a rule:

> **W2.** An agent inside a stage never asks. A script asks, explicitly, via
> `ctx.approval`/`ctx.question`, at points its author chose.

This keeps the failure mode loud (a parked run, visible per §8.6) instead of
silent (a wedged child).

### 8.9 Cancellation

`Esc` interrupts the **turn**, which per W1 is not the run. So the run needs
its own kill:

- `/workflow kill <run_id>` (and `agora workflow kill`), cancelling the
  run-scoped context; §3's cancellation already propagates to in-flight
  children.
- `/workflow ps` to find the id — scoped to THIS session's runs (see below).

**A live run dies with the session (operator decision, 2026-07-27).** Session
exit cancels the run-scoped context, which §3's cancellation already propagates
to in-flight children. Survive-and-reattach was considered and rejected as
awkward: it buys a reconnect UX nobody asked for and pays for it with orphaned
runs, cross-session discovery, and a liveness question the graph deliberately
does not answer (§8.4).

Three consequences, all simplifications:

1. **No cross-session run discovery.** `ps` lists the current session's runs;
   there is no "attach to someone else's run". `~/.agora/workflow-runs/` stays a
   record, not a registry of live objects.
2. **The reconnect story is resume, not reattach.** §4's journal already gives
   the better version: a killed run replays its journal on
   `--resume <run_id>`, so the longest matching prefix returns cached results
   instantly and only the unfinished tail re-runs. Picking work back up costs
   the tail, not the run — and it works across days, not just across a
   reconnect.
3. **§8.9's runaway risk disappears.** A run the operator has lost sight of
   cannot outlive the window they lost sight of it in.

**Teardown must be reliable, and that is the load-bearing requirement here.**
Killing the run's context is not enough on its own — every spawned child must
actually die with it. `internal/subagent`'s `CancelNode` already blocks until a
run observes cancellation rather than flipping status optimistically, so the
machinery exists; the in-session lane must use it on exit and must not return
from teardown until children are done. An exit path that leaves orphaned agent
processes burning tokens is strictly worse than the survive-and-reattach model
this decision rejected.

Persisted outcomes (agora#158) make the aftermath legible: children cancelled
by session exit record `interrupted`, so a later `--resume` or a post-mortem
can tell them apart from ones that failed on their own.

### 8.10 Cost visibility is a gate on this feature, not a nicety

`ctx.budget` is a stub — `total`/`spent`/`remaining` report unbounded (#106).
A surface that lets an operator start 1000 agents in one keystroke, with no
running cost display, should not ship.

**Concurrency ceiling: default 5, configurable (operator decision,
2026-07-27).** `workflow.max_concurrent_agents` in `config.json`, resolved
through the same user→project precedence as `default_effort`. It lowers the
engine's existing cap (§3's `min(16, cores-2)`) for workflow runs; the two
compose as a minimum, so the ceiling can only ever tighten, never widen past
what the machine can take.

Why concurrency is the right lever for a *default*: it bounds the burn RATE,
which is what makes a mistake cheap to notice and cheap to stop. A run that
would have spawned 40 agents at once instead spawns 5, and the operator sees
the cost climbing with 35 still queued and time to hit `/workflow kill`. Five
is deliberately conservative — the point of a default is to be safe before
anyone knows what these runs cost.

**Still needed, and NOT the same thing:** a total-agents ceiling per run. The
concurrency cap bounds rate, not total spend — a loop-until-dry pattern (§6)
can run 500 agents five at a time and cost exactly as much as 500 at once. The
§3 lifetime cap (1000) is a runaway backstop, not a cost control. v1 must
therefore also display a **live agent count and elapsed** while a run is
active (§8.6), so an operator can see 200 agents deep and act. A configured
total ceiling should follow once `ctx.budget` can express cost rather than
count.

Recorded as a build-order constraint because it is the one part of this that
can lose real money.

### 8.11 Sizing

**v1**: `/workflow <name>` + `ps` + `kill`; run-as-background-object (W1);
hosting via the shared seam (§8.3); `ItemWorkflowProgress` emitted (§8.5);
status-row + transcript rendering incl. the parked indicator (§8.6); agent
count display (§8.10).

**Deferred**: `watch` as a full-screen tree; per-agent token rows; model-callable
invocation (§8.1); surviving a daemon restart as a live waiting object (§7
already defers this — v1 recovers a parked run by journal replay).

**Decided by the operator (2026-07-27):**

1. **A live run dies with session exit** — no survive-and-reattach (§8.9).
2. **Concurrency ceiling defaults to 5, configurable** (§8.10).

**Still open:**

3. **Whether the headless `agora workflow run` lane is consolidated onto the
   shared seam now or later** (§8.3). Consolidating is more work and removes a
   real drift; deferring keeps two lanes with different tool surfaces.
4. **Does the "5" in §8.10 cap concurrent agents or total agents per run?**
   Specced as CONCURRENT, because a total cap of 5 would make most of §6's
   pattern library (adversarial verify with N skeptics, judge panels,
   loop-until-dry) unusable — those routinely want more than five agents across
   a run, just not five at a time. If a total cap was meant, it is a one-line
   default change plus the refusal path in §8.10, and §6 needs revisiting.
