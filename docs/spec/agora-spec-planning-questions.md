# agora spec — planning & questions

Settled conversationally with the operator 2026-07-16. Planning = the **design → spec → plan** stages of the work, not a todo list; questions are **missing information**, never fabricated, and travel a defined escalation ladder. Both reuse existing machinery: the plan gate and the question kind ride the approvals pipeline (agora-spec-approvals), the plan artifact is a thread item (agora-spec-persistence), parked questions ride the io seam (agora-spec-io).

## 1. Planning — phases and the plan artifact

**Planning covers design, spec, and plan** (operator). Its output is an **artifact set that scales with the work**:

- small work: a `plan` thread item — steps + open questions;
- big work: design notes + a spec doc + a **decomposition into work items**, each with **observable acceptance criteria** (per docs/network/OBSERVABLE-CRITERIA.md — planning is where DoD gets authored; the post-hoc acceptance gates downstream are only as good as criteria written here).

**The plan object** is always available, gate or no gate:

- a `plan` tool (codex `update_plan` shape): create/update the current plan; every update is a new `plan` item revision in the thread (append-only, never rewritten);
- streamed to clients as an item type (io §1) — TUI panel / web chip; doubles as the recitation anchor (deepagents finding) and as judge input for acceptance gates;
- schema: `{ phase: design|spec|plan, steps[], open_questions[] (question-shaped, §4), artifacts[] (spec/doc refs), work_items[] ({summary, acceptance_criteria[], suggested_executor}) }` — all fields optional, small work uses `steps` only.

## 2. The planning posture — one posture, two exits

Plan mode and orchestrate mode share the **same PRE-GATE posture**: a policy overlay while planning is active —

- **pre-gate only**: mutating tools hard-deny with reason `"planning"` (the deny message tells the model it is planning, not failing); read/search/context tools auto. There is nothing to "redirect to" before a plan is approved — a mutation attempt during design/spec/plan is simply an error;
- maps to hooks `permission_mode: "plan"` (the alias approvals §3 already reports);
- `plan_model`: planning turns MAY pin a different bridle alias than execution turns (plan on frontier, execute on cheap — the opusplan economics, now a mode knob; composes with `[modes.orchestrate]` planner/executor tiers, subagents §5).

**Two exits on gate approval:**

| exit | what approval triggers | used by |
|---|---|---|
| **inline** (plan mode) | posture drops entirely, same session executes the plan under normal policy | interactive dev |
| **delegate** (orchestrate mode, subagents §5) | approved decomposition feeds `agent()` / workflows / dispatch; `work_items[]` may seed the ledger directly (`workitem create`). Post-gate execution is governed by §5's **redirect discipline** (nudge by default, `hard_enforce` opt-in) — that is an *execution style*, not the planning overlay | orchestrator threads |

Orchestrate mode is therefore *the delegate exit of this posture* — subagents §5 defines the delegation doctrine and economics; this chapter defines the pre-gate posture and the gate.

## 3. The plan gate — an approval kind, opt-in

- New approval kind **`plan`** on the standard (kind, decision, scope) model: raised by the model calling the `plan` tool with `submit: true` — submitting signals readiness, it never exits the posture by itself. Payload = the plan artifact (+ unresolved `open_questions`, rendered with the gate card). `allow` → the chosen exit runs. `deny` + message → **revision loop**: posture stays, model revises, re-raises. All approvals machinery comes free: fan-out, first-answer-wins, `approver` capability, audit line, timeout→deny (a gate IS a security gate — contrast §6).
- **Exit authority is the operator's** (operator, 2026-07-16): the posture ends only on an operator act — approving the gate card, an explicit mode change, or a conversational go-signal ("start the build"), which the harness records as the `allow` decision with the message as evidence (the conversational register applied to the gate). The model proposes exit; the operator disposes.
- **Open-questions invariant**: the gate CANNOT pass while `open_questions` is non-empty — even on an explicit operator exit, unresolved questions are surfaced and resolved first (at the gate is fine: answer the cards, or work them conversationally, then the exit proceeds). An explicit operator delegation — "your call" — is a valid *answer* to a question, not a bypass of it; never-fabricate is preserved because the model now holds a recorded decision, not a guess.
- **Opt-in, with an advisory nudge** (operator): entering the posture is the operator's act (`/plan`, `/orchestrate`, config default per project). The core prompt (agora-spec-prompt §2) instructs the model to **suggest** entering planning when work looks big — multi-file, multi-stage, ambiguous requirements, destructive/cross-cutting — never to enter it unilaterally.
- **One-shot dispatch builders: off.** Their contract is turns=1 + post-hoc acceptance gates; a mid-flight human gate contradicts the design. Headless orchestrators that DO plan route the gate through the parked-question path (§5) — the plan gate card appears in the inbox like any decision item.

## 4. Questions — one kind, two registers

**One approval-pipeline kind `question`** with `source: agent | mcp_server | workflow` (MCP elicitation folds in as `source: mcp_server`; if the SDK's elicitation shape ever fights the richer schema, the fallback is sibling kinds sharing one renderer/pipeline — same UX either way). Resolves with a **structured answer**, not allow/deny:

```jsonc
// question payload
{ "text": "...", "context": "...", "evidence": [refs],
  "options": [{label, description}]?, "multi_select": false, "free_text": true,
  "blocking": true }
// answer
{ "choice": [...]? , "text": "..."? , "by": <identity> }
```

Answering requires the `interactive` capability (remote §4); `plan` and permission kinds keep requiring `approver`.

**Hooks**: questions BYPASS the hook stage in v1 — `PermissionRequest` fires only for permission kinds (its allow/deny shape cannot answer, and must never be bent to try). `QuestionAsked` is reserved as the v1.1 hook event whose output carries a structured answer (hooks spec §1.2) — operator-authored auto-answers, falling through to the ladder when silent.

**The verb: one harness-intrinsic tool, `question(payload, blocking=true)`** (operator, 2026-07-16) — available in EVERY profile; the escalation ladder (§5) supplies context-appropriate semantics, so the model never needs to know where it's running: interactive thread → modal/park; one-shot pod → the harness converts the call into the `blocked: needs-input` termination (honest-blocked falls out of the harness, not per-profile prompt rules); subagent/workflow child → bubbles to the parent. Not named `ask_user` because the target isn't always a user — it's whoever sits one level up. `blocking: false` = file-and-continue (the inbox batching path). `question` joins the harness-intrinsic core-tool class (`plan`, `question`, `tool_search`, `memory.*` — mcp §5a).

**Two registers — mechanism vs style (operator, 2026-07-16):**

- **Conversational** (the core-prompt contract; agora-spec-prompt §2): for design/open questions in interactive contexts — especially the planning posture — the model does NOT raise the structured kind. It asks in prose: **one thing at a time, in dependency order; leaning + reasoning stated first; invite counter; converge on shared understanding; push back when it has better information.** No form, no modal.
- **Structured cards** (the `question` kind proper): for questions that **outlive the conversation** — enumerable mid-execution blockers, MCP elicitation, workflow input stages, and everything **parked** (§5). A card is self-contained: context + evidence + options + free-text, answerable cold hours later.

They meet at the plan gate: interactive planning works questions conversationally as it goes; whatever is still unresolved at gate time attaches to the plan artifact as structured `open_questions`, so the same plan reviews cleanly from the inbox. Walk-through-with-shadow = the conversational register applied to the queued cards; register conversion at the top of the ladder is normal (§5).

## 5. The escalation ladder

**Missing information travels UP one level; answers travel back DOWN as context — never as a live wire into a sleeping process.**

| level | on a blocking question | on answer |
|---|---|---|
| **interactive / orchestrator thread** | **park**: thread → `waiting-on-answer` (durable, survives daemon restart + detach), question becomes a queued card (the needs-jacinta inbox reads the daemon's parked-question queue) | answer resumes the thread; the answer arrives as the next turn's input |
| **dispatch job (headless pod)** | **die honestly**: terminate with typed result `blocked: needs-input {question}`; lease releases immediately — no sleeping pod holds a work item | whoever dispatched (orchestrator or operator inbox) answers → **re-dispatch with the answer folded into the brief**. Token loss accepted and small in practice (prefix caching, thread-seeded briefs per NEX-451, workflow journals) |
| **subagent / workflow child** | **bubble**: the question surfaces to the parent through the agent/workflow result channel; parent answers from its own context (it usually can), escalates up the same ladder, or fails the branch | subagent: continuation (`send_message`) or respawn with the answer in the prompt; workflow: journal resume replays the unchanged prefix and re-runs the questioning stage with the answer injected |

- `blocking: false` questions skip straight to the queue at every level — the thread/job continues other work; the answer arrives as a later inbox item (the mediated-roundtable contract made mechanical: batched decision-points, never a question firehose).
- **Register conversion at the top**: a child's structured question reaching the operator interactively is *presented conversationally* by the orchestrator; reaching them asynchronously it stays a card.
- Under `never-escalate` (headless preset) the park/die conversion is immediate, not after a timeout.

## 6. Invariants (the never-fabricate rules)

1. **A question is not a security gate.** Approvals invariant "timeout → deny" does NOT apply: an unanswered question **parks** (or converts per §5); the harness never synthesizes an answer and never lets the model guess-and-continue past a blocking question.
2. A parked thread is durable state (daemon-owned, survives restarts/detach) and visible: `thread.waiting` event, presence in `/status`, a card in the queue.
3. One-shot builders do not ask — they go `blocked: needs-input`. Honest-blocked is already a first-class, gate-respected outcome; parking is for threads, not pods.
4. Every answer is attributed (`by` identity) and audit-lined like any approval decision.
5. Workflows/subagents may answer a child's question only from information they actually hold — bubbling exists precisely so levels don't fabricate downward.
6. **The planning posture never exits with open questions**: `plan` gate `allow` is refused while `open_questions` is non-empty (§3); resolution (including explicit operator delegation) must precede exit. The model never exits the posture unilaterally — exit is an operator act.

## 7. Wire (deltas to agora-spec-io, applied there)

- events: `question.asked {id, source, blocking, payload}` / `question.answered {id, by, answer}`; `thread.waiting {question_id}` / `thread.resumed {question_id}`; item type `plan`.
- input: `{"type":"question_response","id":"...","answer":{...}}`.
- `approval.requested` kind list gains `plan`; `elicitation` is subsumed by `question`.

## 8. Dispatch integration (nexus-side contract)

The broker/dispatch contract gains the typed blocked subclass: builder result `blocked` carries optional `needs_input: <question payload>`; the orchestrator (or drain loop) routes it — to its own context if answerable, else to the operator queue — and re-dispatches the work item with the answer appended to the brief. This is a nexus change, not an agora one; ticket it when the dispatch fabric is next touched.
