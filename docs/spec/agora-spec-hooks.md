# agora spec — lifecycle hooks

Extracted 2026-07-15 from openai/codex @ ~0.145.0-alpha.13 (`~/external/codex/main/codex-rs`), which is itself a reimplementation of Claude Code's hooks. Source citations are codex paths (crate `hooks/`, config schema `config/src/hook_config.rs`, wiring `core/src/hook_runtime.rs`). Format compatibility target: a `hooks.json` written for Claude Code or codex should work in agora unchanged.

## agora build notes (decisions layered on the extraction)

- **Adopt the codex event set (10 events)** — it is a superset of Claude Code's and the additions are the useful ones for agora: `PermissionRequest` (hook-driven auto-approve/deny — this is the seam for the funnel/broker escalation lane and P3c-style operator gates), `SubagentStart/Stop`, `PostCompact`.
- **Only implement `command` handlers first.** Codex parses `prompt`/`agent` handler types but skips them; same call for agora v1.
- **Implement `async: true` properly** (codex parses it and drops with a warning). Go makes this trivial (goroutine + no result wait); it's a real gap in both parents.
- **Skip the MDM/enterprise/legacy-managed layers.** Single-owner sovereign deployment needs: User layer (`~/.agora/hooks.json` + config TOML) and Project layer (`<project>/.agora/hooks.json`), lowest-precedence-first. Keep the layer enum extensible.
- **Adopt the trust model** (content-hash trust of project hooks) — it is cheap and it protects against a cloned repo executing arbitrary hooks on session start. See §4.4.
- Keep the Claude-compat env aliases (`CLAUDE_PLUGIN_ROOT` etc.) so imported plugin hooks run.

---

## 1. Config format (hooks.json / TOML)

### 1.1 Top-level file shapes

`hooks.json` (deny unknown fields):

```jsonc
{
  "description": "optional string",
  "hooks": { /* event map, §1.2 */ }
}
```

TOML in the main config: the `[hooks]` table holds the same event map flattened, plus a `state` sub-table (`hooks.state`, map of positional-key → `{ enabled?: bool, trusted_hash?: "sha256:..." }`).

### 1.2 Event map — 10 events, PascalCase keys, each a list of matcher groups

`PreToolUse`, `PermissionRequest`, `PostToolUse`, `PreCompact`, `PostCompact`, `SessionStart`, `UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `Stop`.

Matchers apply to 8 of them; `UserPromptSubmit` and `Stop` ignore matchers (always run).

**Reserved (v1.1, agora extension): `QuestionAsked`** — the `question`-kind analogue of PermissionRequest, differing in exactly one way: its `hookSpecificOutput` carries a structured `answer` matching the question's schema (never allow/deny); no answer → fall through to the escalation ladder. Use case: operator-authored auto-answers ("whenever anything asks which registry, the answer is X") — a recorded operator decision delivered by machinery, not fabrication. **In v1, `question`-kind items BYPASS the hook stage entirely**: PermissionRequest fires only for permission kinds — its allow/deny shape cannot answer a question, and routing questions through it would violate the never-fabricate invariant (agora-spec-planning-questions §4/§6).

### 1.3 MatcherGroup

```jsonc
{ "matcher": "optional string", "hooks": [ /* handler configs */ ] }
```

### 1.4 Handler config — tagged by `type`

`command` (the implemented one):

```jsonc
{
  "type": "command",
  "command": "shell string",          // required
  "commandWindows": "…",              // optional per-OS override (alias command_windows)
  "timeout": 600,                     // optional, SECONDS, default 600, floor 1
  "async": false,                     // default false (agora: implement)
  "statusMessage": "running my hook"  // optional UI string
}
```

`{ "type": "prompt" }` and `{ "type": "agent" }` exist in the schema for compat; parse-and-skip in v1.

### 1.5 Matcher semantics

- `null`, `""`, `"*"` → match everything.
- If the matcher contains only `[A-Za-z0-9_|]` → **exact/alternation mode**: split on `|`, whole-string equality (`Bash` does not match `BashOutput`; `Edit|Write` matches either; `mcp__memory__create_entities` is a literal).
- Otherwise → **regex mode**, UNANCHORED match (`^Bash` matches `BashOutput`; anchor explicitly). Invalid regex → drop the group at discovery with a warning.
- Matched string per event: tool events → tool name (+ internal aliases, e.g. `apply_patch`↔`Write`/`Edit`); `SessionStart` → source (`startup|resume|clear|compact`); `Subagent*` → agent_type; `Pre/PostCompact` → trigger (`manual|auto`).
- A handler with alternation fires **once per event**, not once per matched alias.

## 2. Per-event I/O contract

**Common stdin fields** (all events): `session_id`, `cwd`, `transcript_path` (nullable), `hook_event_name`, `model`, `permission_mode` (`default|acceptEdits|plan|dontAsk|bypassPermissions`). Turn-scoped events add `turn_id` (codex extension over Claude). Events that can fire inside a subagent add optional `agent_id`/`agent_type`. Field names snake_case.

**Common stdout ("universal") fields** (camelCase): `continue` (default true), `stopReason`, `suppressOutput` (accepted, mostly ignored), `systemMessage` (surfaced as warning in transcript).

**Exit-code convention (uniform):**
- exit 0 → parse stdout as JSON object if it looks like JSON (malformed JSON → Failed); plain text → ignored, EXCEPT SessionStart/SubagentStart/UserPromptSubmit where plain text becomes `additionalContext`.
- **exit 2 → block/deny, with trimmed stderr as the reason** (empty stderr → Failed).
- any other exit → Failed (non-fatal: recorded, does not block).

### 2.1 PreToolUse
Input: common + `turn_id`, `tool_name`, `tool_input` (arbitrary JSON args), `tool_use_id`.
Output (preferred): `hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "allow"|"deny"|"ask", permissionDecisionReason, updatedInput, additionalContext }`.
- `deny` → blocks the tool call; requires non-empty reason; block message is returned to the model as the tool result.
- `allow` is only valid **paired with `updatedInput`** (rewrites the tool args before execution); bare `allow` and `ask` → Failed in codex. (agora: consider honoring bare `allow` as skip-approval; flag as deliberate deviation if so.)
- Legacy top-level `decision:"block"` + `reason` also blocks (Claude compat).
- `additionalContext` → injected as developer message.
- Universal `continue:false`/`stopReason`/`suppressOutput` rejected for this event.
- Aggregation across matched handlers: any block wins; first block reason; `updatedInput` from the LAST-completed handler; block drops any `updatedInput`.

### 2.2 PermissionRequest (codex extension; runs in the approval path before UI)
Input: common + `turn_id`, `tool_name`, `tool_input` (no `tool_use_id`).
Output: `hookSpecificOutput: { hookEventName: "PermissionRequest", decision: { behavior: "allow"|"deny", message, updatedInput?, updatedPermissions?, interrupt? } }`.
- `allow` → auto-approve; `deny` → auto-reject (default message if empty); no decision → fall through to normal approval flow.
- `updatedInput`/`updatedPermissions`/`interrupt:true` → **fail closed** (reserved).
- exit 2 → deny with stderr message.
- Aggregation: any deny wins immediately; else highest-precedence allow; else none.

### 2.3 PostToolUse
Input: common + `turn_id`, `tool_name`, `tool_input`, `tool_response`, `tool_use_id`.
Output: universal + `decision:"block"` + `reason` (feedback to model), `hookSpecificOutput.additionalContext`, `updatedMCPToolOutput` (unsupported, fails open).
- block requires non-empty reason; `continue:false` → stops turn with reason.
- Aggregation: any block; feedback joined `\n\n`; contexts flattened.

### 2.4 / 2.5 PreCompact / PostCompact
Input: common + `turn_id` + `trigger` (`manual|auto`); no `permission_mode`.
Output: universal only. `continue:false` → abort/halt compaction. Any `decision` key → invalid.

### 2.6 SessionStart
Input: `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `model`, `permission_mode`, `source` (`startup|resume|clear|compact`). No `turn_id`.
Output: universal + `hookSpecificOutput.additionalContext`; plain stdout becomes additionalContext; `continue:false` honored (stops).

### 2.7 UserPromptSubmit
Input: common + `turn_id` + `prompt`. Matcher ignored.
Output: plain stdout or `additionalContext` → context injection; `decision:"block"` + reason → blocks the prompt (stop, reason surfaced); `continue:false` → stop; exit 2 → block with stderr.

### 2.8 SubagentStart
Input: `session_id`, `turn_id`, `transcript_path`, `cwd`, event name, `model`, `permission_mode`, `agent_id`, `agent_type`.
Output: like SessionStart but `continue:false` is IGNORED (context-injection only).

### 2.9 / 2.10 SubagentStop / Stop
Input: common + `turn_id`, `stop_hook_active` (bool), `last_assistant_message` (nullable); SubagentStop adds `agent_transcript_path`, `agent_id`, `agent_type`. Matcher ignored for Stop.
Output: `decision:"block"` + reason → the reason becomes a **continuation prompt**: the turn continues (model re-prompted) with `stop_hook_active=true` (loop guard). `continue:false` → end turn, OVERRIDES block. exit 2 → block with stderr as continuation prompt.
Aggregation: any stop wins; else any block, reasons joined `\n\n`, continuation fragments concatenated in declaration order.

## 3. Execution semantics

- **Selection**: all handlers for the event whose matcher matches, in declaration/discovery order (global monotonic counter across layers).
- **Run concurrently**, results re-sorted to configured order for reporting; completion order is tracked only to pick PreToolUse's last-finished `updatedInput`.
- **Invocation**: via shell — unix `$SHELL -lc <command>` (fallback `/bin/sh`), windows `%COMSPEC% /C` (fallback `cmd.exe`). stdin = event JSON (write, close). stdout/stderr captured. Child killed on drop.
- **cwd** = the turn's cwd. **Env** = inherited + per-handler env; plugin hooks get `PLUGIN_ROOT`/`CLAUDE_PLUGIN_ROOT`/`PLUGIN_DATA`/`CLAUDE_PLUGIN_DATA`; `${KEY}` substitution into the command string at discovery time.
- **Timeout**: per-handler seconds, default 600, floor 1; on timeout kill child, run = Failed.
- Serialization failure of the stdin payload → all matched handlers reported Failed for that event.
- Run status vocabulary: Running/Completed/Failed/Blocked/Stopped. Output kinds: Context/Feedback/Stop/Warning/Error. Scope: SessionStart+SubagentStart = thread-scoped; all else turn-scoped.

## 4. Discovery & layering

### 4.1 Order (lowest → highest precedence)
1. Managed/required hooks (agora: optional; keep the slot for broker-pushed policy hooks).
2. Config layers lowest-precedence-first; per layer BOTH `hooks.json` (in the layer's dot-dir) and inline TOML `[hooks]` are loaded (warn if both non-empty).
3. Plugin hook sources last.

### 4.2 File locations (agora mapping)
- User: `~/.agora/hooks.json` + `[hooks]` in user config.
- Project: `<project>/.agora/hooks.json`. Codex nicety worth keeping: linked git worktrees can redirect hook discovery to the root checkout's dot-dir.
- Each folder loaded once (visited-set).

### 4.3 Managed policy (codex: MDM/enterprise — agora: reserve)
`allow_managed_hooks_only` flag → only managed sources load. Managed hooks are always enabled and trusted. For agora this maps to broker/custodian-pushed hooks later; do not build the MDM plumbing.

### 4.4 Trust & enable state
- Positional key per handler: `"{source_path_or_pluginid:relpath}:{event}:{group_index}:{handler_index}"`.
- Content hash over normalized identity (event + matcher + normalized command) — equal hooks from TOML and JSON converge.
- State (`enabled`, `trusted_hash`) read only from User(+session) layers; later layers merge field-by-field.
- Run only if `enabled && (bypass_trust || managed || hash matches trusted_hash)`; mismatch → status Modified; absent → Untrusted. Untrusted/modified hooks are still LISTED for the UI (so the TUI can offer "trust this hook").

## 5. Claude Code compatibility deltas (codex → inherit into agora)

- New events vs Claude: `PermissionRequest`, `PostCompact`, `SubagentStart` (Claude has SubagentStop only), i.e. 10 vs Claude's 7-ish.
- `turn_id` on turn-scoped inputs, `agent_id`/`agent_type` optionals — codex extensions; harmless to Claude-authored hooks.
- Claude fields accepted-but-unsupported: PreToolUse `permissionDecision:"ask"`, `decision:"approve"`; PermissionRequest `updatedInput`/`updatedPermissions`/`interrupt` (fail closed); PostToolUse `updatedMCPToolOutput` (fail open); `suppressOutput` ignored.
- `commandWindows` is a codex addition (per-OS command).
- Trust-hash gating is codex-specific (Claude doesn't gate); agora adopts it.
- Handler types `prompt`/`agent` schema-present, unimplemented.
