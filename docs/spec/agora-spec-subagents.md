# agora spec — subagents

Decision (operator, 2026-07-15): base on **Claude Code's model**, judged better than codex's. Codex's surface documented below for comparison/import. Spec'd for Go on funnel — a subagent is a child funnel session with its own context window.

## 1. Agent definitions — markdown + frontmatter (Claude Code format, adopt verbatim)

Location: `<project>/.agora/agents/*.md` and `~/.agora/agents/*.md` (also read `.claude/agents/*.md` for import/compat — shadow already has builder/reviewer/security-validator there). One file = one agent type.

```markdown
---
name: reviewer
description: Review stage for completed code changes — checks correctness, bugs,
  edge cases... Use after builders complete.   # ROUTING TEXT: the orchestrator model
                                               # sees this when choosing an agent
tools: Read, Glob, Grep, Bash                  # allowlist; omit = all tools
model: sonnet                                  # optional override; omit = inherit parent
effort: high                                   # optional reasoning-effort override
---
(body = the agent's system prompt)
```

Key insight to preserve: the **description is written for the calling model** (when to delegate to this agent), the **body is written for the agent** (how to do the job). Built-ins: a `general-purpose` agent (all tools) and an `explore` agent (read-only search) cover most delegation without any user definitions.

## 2. The spawn tool (agora's `agent` tool)

One tool, not a namespace of five:

```
agent(prompt, opts?: {
  agent_type?: string,      // default "general-purpose"
  model?, effort?,          // overrides, validated against available models
  run_in_background?: bool, // default true: returns immediately with agent_id;
                            // completion delivered as a turn event/notification
  isolation?: "worktree",   // fresh git worktree for parallel mutators; auto-removed if unchanged
  schema?: JSONSchema       // force structured output: child must call a
                            // StructuredOutput tool matching schema; validated, retried on mismatch
})
```

Semantics:
- **Fire-and-collect**: the child's final message IS the return value (children are told this — they return raw data, not prose for a human). No mid-flight chatter by default.
- **Background + notification**: parent turn continues; when a child finishes, the parent is re-invoked with a task-notification containing the result. Parent can also block (`run_in_background:false`) when the result gates the next step.
- **Continuation**: `send_message(agent_id, message)` re-opens a *finished* agent with its context intact — the lean version of codex's resume/send_input. No steering of running agents in v1 (interrupt = kill).
- **Question bubbling**: a child's blocking `question` surfaces to the parent through this result channel (the child ends with a question-shaped result, like the pod `blocked: needs-input` pattern); the parent answers from its own context, escalates up the ladder, or fails the branch — answers flow down via continuation or respawn-with-answer, never into a sleeping child (agora-spec-planning-questions §5).
- **Inheritance**: child gets parent's cwd, approval policy, permission profile, and tool set (minus def's allowlist); model/effort inherited unless overridden. **No conversation history by default** — the prompt is the contract. (codex's fork_turns "all"/last-N is powerful but blurs the contract; add later if wanted.)
- Depth cap (default 1 — subagents can't spawn subagents unless enabled) and per-session spawn cap. Assign readable nicknames from a pool for UI.
- Parallel spawns: multiple agent() calls in one assistant turn run concurrently; cap concurrent children at min(16, cores-2), queue the rest.

## 2a. Cancellation propagation (closes coherence hole #7)

- **Children are owned by the thread, not the turn.** Interrupt (Esc / `interrupt` message) cancels the parent's in-flight sampling and any **foreground** child waits (`run_in_background: false` spawns are cancelled with the turn — the parent was blocked on them). **Background** children keep running and deliver their completion notifications normally; the operator closes them explicitly if unwanted.
- **Workflow runs are owned by the run**, not the invoking turn: parent interrupt never kills a background workflow; `workflow stop|pause <run_id>` is the explicit verb. Stopping a run cancels its in-flight agents (journal keeps completed prefixes for resume).
- **Thread archive/delete and pod `deprovision` cancel everything downstream**: BFS over the agent graph (open edges), plus any workflow runs rooted in the thread. Cancellation is graceful: child receives cancel, in-flight tool calls abort, transcript marked `interrupted` — a cancelled child is resumable-by-continuation like any finished agent.

## 3. Persistence

- Each child is a full thread in the thread store (agora-spec-persistence; replayable transcript, `agent-<id>.jsonl` or funnel equivalent).
- **Agent graph**: persist parent/child edges `(parent_thread, child_thread, status: open|closed)`; queries: direct children, BFS descendants, optional status filter (a closed edge hides its subtree). This is codex's `agent-graph-store` — tiny and worth having from day one; it's what makes the TUI's agent tree and workflow progress views possible.

## 4. codex comparison (for the record / import)

- v1 namespace `multi_agent_v1`: spawn_agent (message|items, fork_context, model/effort/tier) → {agent_id, nickname}; send_input (interrupt?|queued); wait_agent(targets[], timeout_ms) → status map; close_agent (closes descendants too); resume_agent. v2 flattens to top-level tools with hierarchical task paths (`/root/task1/task_3`), fork_turns none|all|N, send_message (queue, no turn) vs followup_task (triggers turn if idle), list_agents, interrupt_agent. Status vocab: pending_init | running | interrupted | shutdown | {completed} | {errored} | not_found.
- codex agent_type = a **role TOML** overlay (config-shaped keys: model_reasoning_effort, developer_instructions, ...) loaded as a high-precedence config layer — a different philosophy (config overlay) from Claude's (system-prompt + tool allowlist). agora sticks with the Claude shape; a role-TOML is expressible as frontmatter fields if ever needed.
- codex skills-as-agents (`agents/openai.yaml`, allow_implicit_invocation:false): recognize on import, convert to an agent .md.

## 5. Orchestration mode (operator-requested, 2026-07-15)

A session mode where **the active model is the planner; execution happens through subagents/workflows**. This bakes shadow's `orchestrator` skill / opusplan pattern into the harness instead of leaving it as prompt convention. Orchestrate mode is the **delegate exit of the planning posture** (agora-spec-planning-questions §2): pre-gate it shares the planning overlay (mutations hard-deny), and exit is operator-authorized through the plan gate; POST-gate execution is governed by this section's redirect discipline (nudge by default, `hard_enforce` opt-in) — an execution style, distinct from the planning overlay. This section owns the delegation doctrine and economics.

- **What the planner keeps**: read/search/context tools (Read, Grep, Glob, read-only shell), the `agent()` tool, workflow invocation/authoring, and plan/synthesis output. **What gets redirected**: mutating tools on the main thread (file edits, non-read shell) — by default a *nudge* (tool returns a "delegate this via agent() or a workflow" redirect the model must acknowledge), with `hard_enforce = true` upgrading to a hard block-with-reason. Subagents and workflow children are unaffected — their agent defs govern them.
- **Injected doctrine** (developer-role fragment while the mode is on): decompose the task; bounded/mechanical/parallelizable pieces → `agent()` on a cheap alias; multi-stage, fan-out, or repeatable shapes → invoke a saved workflow, or author one and run it; verify results yourself; synthesize; never re-derive what a child already returned. Choosing workflow-vs-ad-hoc-agent is the planner's judgment call — the doctrine gives the heuristic, not a rule.
- **Model economics built in**: mode config pairs planner and executor tiers —
  ```toml
  [modes.orchestrate]
  planner_effort          = "high"
  default_executor_model  = "local-heavy"   # alias; children inherit unless call/phase/def overrides
  default_executor_effort = "medium"
  hard_enforce            = false
  ```
  The planner stays on the session (frontier) model; delegated work defaults to local/cheap tiers — the frontier context window holds the plan and the judgments, not the diffs. Resolution order from workflows §2a is unchanged; this only supplies the mode-level default at the bottom of the chain (above plain session inherit).
- **Surfaces**: `/orchestrate` toggle in the TUI (footer badge), `agora --orchestrate` at launch, per-project default in config. Mode state is session-scoped and visible in `/status`.
- **Composes, not conflicts**: `%alias:effort` one-shot overrides still apply to the planner turn; PermissionRequest hooks still run for children; approval modal unchanged (the planner asking to run a workflow is not an approval event — the children's actions are, per normal policy).

## 6. AGENTS.md / context docs (companion contract, lives with subagents since children inherit it)

Discovery (codex algorithm, adopt): find project root = nearest ancestor of cwd with a root marker (default `.git`; configurable, project-layer excluded from defining its own markers). Collect docs **root → cwd inclusive** (root-down order). Per directory, first hit of: `AGENTS.override.md` > `AGENTS.md` > configured fallbacks (agora adds `CLAUDE.md` as a fallback filename for compat). Total budget 32 KiB default (0 disables); files consumed in order, last one truncated to fit; empty files skipped.

Injection: user-level instructions first, then project docs joined with a one-time `\n\n--- project-doc ---\n\n` separator; rendered as a user-role fragment wrapped in `# AGENTS.md instructions for <dir>\n\n<INSTRUCTIONS>\n...\n</INSTRUCTIONS>`. Cache keyed on environment/cwd snapshot; recompute only on change.
