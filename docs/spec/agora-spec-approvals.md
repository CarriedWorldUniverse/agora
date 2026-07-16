# agora spec — unified approval policy

Closes coherence hole #1 (2026-07-15, operator delegated the design). One canonical model; every other vocabulary in the spec set is a declared alias of this.

## 1. The model

An approval situation is a triple **(kind, decision, scope)**.

**Kinds** (what is being asked):
| kind | raised by |
|---|---|
| `exec` | command execution beyond the sandbox-safe set |
| `patch` | file changes (writes inside wd normally auto per sandbox policy; this covers protected paths + explicit patch review modes) |
| `escalation` | anything outside the sandbox envelope: write outside wd, protected paths (.git/.agora/.cairn destructive), network beyond policy, wasm module import beyond its declared grants (mcp §1a) |
| `mcp_tool` | MCP tool call per server/tool approval mode |
| `question` | missing information — the agent mid-turn, an MCP server (elicitation, subsumed as `source: mcp_server`), or a workflow input stage; resolves with a structured ANSWER, not allow/deny (agora-spec-planning-questions §4) |
| `plan` | the plan gate closing a planning posture; payload = the plan artifact + unresolved open_questions (agora-spec-planning-questions §3) |
| `gate` | workflow approval gate (`ctx.approval`, workflow-engine v1 — workflows §2) |

**Decisions**: `allow` | `deny` (+ `message` on deny = feedback to the model). Kind `question` resolves with a structured **answer** instead (options/multi-select/free text; decline-with-message maps to deny). `plan` deny + message = the revision loop.

**Scopes** (on allow): `once` (default) | `session` | `prefix:<cmd-prefix>` (exec only) | `host:<pattern>` (network only). Scoped allows persist per-thread (session) or per config amendment (prefix/host may be persisted to policy files, execpolicy-style, with an explicit "save" flag).

## 2. Policy — what gets decided *before* a human is asked

Per-kind policy value: `auto` (decide allow if within envelope, without asking) | `prompt` (ask) | `deny` (refuse flat). A **policy set** = map of kind → value + the sandbox envelope (io spec §3a) it presumes. Decision stage 2 of the canonical pipeline (remote §8: hooks → **policy** → approvers → queue → timeout).

**Named presets** (what profiles reference):
| preset | exec | patch | escalation | mcp_tool | question | plan |
|---|---|---|---|---|---|---|
| `prompt` (default dev) | prompt | auto* | prompt | per-server | prompt | prompt |
| `auto-safe` (chat) | auto-within-sandbox | auto* | prompt | per-server | prompt | prompt |
| `strict` | prompt | prompt | prompt | prompt | prompt | prompt |
| `never-escalate` (headless/pod default) | auto-within-sandbox | auto* | deny | per-server | convert† | deny |

\* patch auto = writes inside wd are the sandbox's job, not an approval; protected paths still raise `escalation`.

† `question` under never-escalate converts immediately per the escalation ladder (park for threads, `blocked: needs-input` termination for one-shot pods — agora-spec-planning-questions §5); it is never auto-answered and never silently denied. `plan` deny headless = the posture isn't gateable there; headless orchestrators that plan route the gate through the parked queue instead.

Presets are definable in config (`[approval_presets.<name>]`); the four above ship built-in.

## 3. Alias mappings (declared, exhaustive)

- **Profile** `approval = "<preset>"` → the preset (existing profile strings `prompt`/`auto-safe` are presets).
- **Pipe/exec flag** `--approval-policy`: `auto` → `never-escalate` preset (never blocks on a human); `deny-mutations` → preset with exec/patch/escalation = deny, reads run; `escalate` → `prompt` preset with stage-3 fan-out to remote approvers + queue (nothing auto-denied while a doorbell is possible).
- **MCP per-tool** `approval_mode`: `auto` → mcp_tool auto; `prompt` → prompt; `writes` → auto for read-annotated tools, prompt for the rest; `approve` → auto-allow (trusted tool).
- **Hooks stdin `permission_mode`** (codex/CC compat reporting): `default` when preset==prompt/strict, `bypassPermissions` when never-escalate/auto within a bypass flag, `acceptEdits` when patch==auto and exec==prompt, `dontAsk` when preset==never-escalate, `plan` when the planning posture is active (plan/orchestrate — agora-spec-planning-questions §2, mutations auto-denied `"planning"`). Report-only — hooks never *configure* via this field.
- **TUI approval modal options** map to decisions+scopes: "approve once"=(allow,once), "approve for session"=(allow,session), "don't ask for prefix"=(allow,prefix) *(TUI v1.1 — deferred per agora-spec-tui §3; the wire scope exists in v1)*, "deny and tell agora"=(deny,message). Kind `question` renders as a **question card** (options/multi-select/free-text), not the allow/deny modal; answering a question needs `interactive`, while `plan` and the permission kinds keep requiring `approver`.

## 4. Invariants

1. `deny` at policy stage is final for that call (model gets the reason; may adapt) — hooks (stage 1) can only be *more* restrictive than policy, except an explicit PermissionRequest-hook `allow`, which is an operator-authored bypass and is logged as such.
2. Timeout fallback is always `deny` (or fail-turn), never allow (remote §8) — for the permission kinds and `plan` (a gate is a security gate; a denied plan just re-enters revision, nothing mutated). Kind `question` is the exception: an unanswered question **parks/converts, never synthesizes an answer and never deny-fabricates** (agora-spec-planning-questions §6).
3. Every decision is recorded with its stage + actor (hook name / preset / device identity) — the audit line.
4. Subagents inherit the parent's effective policy set; workflows may *restrict* per stage, never widen.
5. Capability `approver` (remote §4) gates stage-3 answering; `admin` gates changing presets at runtime.
