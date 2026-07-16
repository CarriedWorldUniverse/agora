# agora spec — chainable I/O channels (agora as the driver)

Operator requirement (2026-07-15): the harness must be **chainable, with agora as the driver of the chain** — a clean input/output contract so agora runs as the hub and any number of sources/sinks attach to it: a chat webpage, vessel for live voice, the operator directly in the TUI — concurrently, against the same sessions. Reference contract extracted from codex `exec/src/exec_events.rs` (JSONL event schema) + the app-server seam lesson.

## 0. Principle

**agora owns the sessions; everything else is an attachable client.** The core is a duplex event stream; frontends are interchangeable and concurrent:

| shape | transport | consumer |
|---|---|---|
| **daemon** (primary) | unix socket + websocket (+ http for the web client) | TUI, chat webpage, vessel, nexus dispatch — attached simultaneously |
| **pipe mode** | stdin/stdout JSONL | shell chains, one-off scripting |
| **library** | Go channels in-process | embedding agora in another Go binary |

Pipe mode is the session protocol flattened to one implicit session and one client — same event types, no method envelope. Nothing is TUI-only: if the TUI can render it, any attached client can receive it.

**Terminology** (used across all specs): a **thread** is one persisted conversation (wd-linked, resumable — what the operator calls a session; `/resume` resumes threads); an **attachment** is a client's live connection to the daemon; "session store" = the thread store. Wire ids are `thread_id`/`turn_id`.

## 0a. Daemon + multi-attach (the driver shape)

`agora daemon` runs long-lived, holds all threads, listens on UDS (local) and ws (loopback by default; tailnet/herald-authed for remote web later). Attach semantics:

- **Concurrent clients per thread**: web page, vessel, and TUI can all attach to the *same* thread. Output events fan out to every attached client; input is accepted from any client with the `interactive` capability (capability model — `observer`/`interactive`/`approver`/`admin` — is defined once in agora-spec-remote §4; clients declare theirs at attach and enrollment caps what they may declare). Mid-turn input follows normal steer/queue discipline regardless of which client sent it. (Config/preset/mode changes are the exception — `admin`-gated, not `interactive`; see §1 `config`.)
- **Presence events**: `client.attached {client_id, kind, capabilities}` / `client.detached` broadcast on the thread, so the TUI can show "vessel is listening" and vessel can duck when the operator starts typing.
- **Approval routing**: `approval.requested` fans out to all `approver`-capable clients; **first answer wins**, everyone else receives `approval.resolved {by}`. A voice client can answer an approval the TUI displayed, and vice versa.
- **Attach/detach is free**: threads and running turns survive client disconnects (the daemon is the state owner). Reattach replays a tail of recent items (`attach {thread_id, replay: N}`) so a client can render context.
- **TUI fallback**: if no daemon is running, the TUI spawns the core in-process (same client facade over Go channels — codex's in-process transport pattern); `agora daemon` upgrades to shared mode. One binary, `arg0`-style dispatch.
- The chat webpage is a thin ws client of this same protocol (agora can serve its static assets). This is the backend the parked ADE web-UI rebuild would sit on — one daemon, chat spine + sessions, no separate server.

## 1. Pipe mode (`agora pipe`)

Long-running duplex process. **stdin**: one JSON object per line (JSONL). **stdout**: JSONL events. stderr: human diagnostics only, never protocol.

### Input messages
```jsonc
{"type":"user_message","text":"...", "model":"frontier","effort":"high"}  // model/effort optional (one-shot, = %-override)
{"type":"steer","text":"..."}          // queued guidance mid-turn
{"type":"interrupt"}                    // barge-in: stop current turn (voice essential)
{"type":"approval_response","id":"...","decision":"allow|deny","scope":"once|session|prefix|host","message":"..."}  // (decision,scope) per approvals §1; scope optional, default once
{"type":"question_response","id":"...","answer":{"choice":[...],"text":"..."}}  // structured answer to a question.asked (planning-questions §4)
{"type":"config","key":"...","value":...}   // mode/preset/daemon config — requires the `admin` capability (approvals §4 invariant 5, remote §4), NOT plain interactive input
{"type":"end"}                          // graceful shutdown (also SIGTERM/stdin EOF)
```
Plain non-JSON lines are accepted as `user_message` text (lenient mode, flag-gated) so `echo "fix the test" | agora pipe` works.

### Output events (JSONL, tagged `type` — codex exec_events shape, extended)
- `thread.started {thread_id}`
- `turn.started {turn_id}` / `turn.completed {usage}` / `turn.failed {error}`
- `item.started` / `item.updated` / `item.completed` — items: `agent_message`, `reasoning`, `command_execution` (+status), `file_change`, `mcp_tool_call`, `plan` (the plan artifact, revisioned — planning-questions §1), `agent_spawn`, `workflow_progress`
- `item.agent_message.delta {text}` — streaming text (subscribable; off by default in pipe mode, on with `--deltas`)
- `tool.loaded {names}` — a `tool_search` load brought new tool schemas into scope (agora-spec-mcp §5)
- **context events** (agora-spec-context §2 contract #4, agora-spec-context-curation): `thread.compaction.started {trigger}` / `thread.compaction.completed {tokens_before, tokens_after}` (summarization episodes); `thread.curation.demoted {keys, tokens_freed}` / `thread.curation.readmitted {key}` (view-only working-set LRU — no thread mutation). Frontends render these as awareness surfaces; off in `--filter` chains.
- `approval.requested {id, kind, payload}` — `kind` is one of the canonical approval kinds (agora-spec-approvals §1: `exec | patch | escalation | mcp_tool | question | plan | gate`). The consumer answers via `approval_response` (kind `question` via `question_response`), or a `--approval-policy` flag pre-decides (`auto|deny-mutations|escalate`)
- `question.asked {id, source, blocking, payload}` / `question.answered {id, by, answer}` — structured questions (planning-questions §4); non-blocking ones queue instead of blocking the turn
- `thread.waiting {question_id}` / `thread.resumed {question_id}` — parked-thread lifecycle: a blocking question with no answer parks the thread durably (survives restarts/detach); the needs-jacinta inbox reads the daemon's parked-question queue (planning-questions §5)
- `error {message}`

(Multi-attach adds two more events on the session protocol, not shown in single-client pipe mode: `client.attached`/`client.detached` presence and `approval.resolved {by}` — §0a. Session lifecycle events like `provisioned {identity_fp, profile}` (remote §6a) also ride §2.)

**Channel filtering for chains**: `--filter agent_message` emits only final agent-message items (each as one JSONL line, full text) — this is the mode a TTS consumer wants: no tool noise, no deltas, speakable units. `--filter text` degrades further to raw text lines (pure Unix pipe: text in → text out).

## 2. Session protocol (unix socket / ws)

The rich seam: multiple threads, thread lifecycle (start/resume/fork/list), turn ops (start/steer/interrupt), streaming items, approvals, fs/config surface — codex app-server v2 is the extracted reference for method granularity. v1 needs only: thread start/resume, turn start/steer/interrupt, item stream, approvals, status. The TUI is client #1; parity rule: **the TUI may not use any call the protocol doesn't expose.**

## 3. The vessel chain (worked example)

Vessel attaches to the daemon as an interactive client (ws/UDS), subscribing with `--filter agent_message`-equivalent options in its attach request — it is a *peer* of the TUI on the same thread, not an upstream pipe:

```
mic → vessel STT ──user_message──▶ agora daemon ◀──▶ TUI (same thread, simultaneously)
vessel TTS ◀──agent_message items ──────┘        ◀──▶ web page
```
- vessel sends `user_message` per finalized utterance; sends `interrupt` on barge-in (user starts talking over TTS), then the corrected `user_message`.
- agora emits final `agent_message` items; vessel speaks them. `turn.completed` = vessel's cue to reopen the mic.
- Approvals in a voice chain: run `--approval-policy escalate` — `approval.requested` events are forwarded (vessel can *speak* the approval question and accept a spoken yes/no → `approval_response`). Sovereign voice-driven approvals fall out of the contract for free.
- Latency note: voice wants sentence-level chunking — a `--chunk sentences` output option on agent_message (emit `item.agent_message.chunk` per sentence) is the one voice-specific accommodation worth building; everything else is generic.

## 3a. Sessions & working directory (operator, 2026-07-15)

**Start dir ≠ working dir.** The daemon/process starts where it starts (`~/shadow` — home base, keyfile, config; a dispatch pod always starts in its fixed pod home). The **working dir is a per-session property**: the workspace the session is *about* (`~/src/cairn` ⇒ "this is a cairn session").

- **Session record links to the working dir**: `working_dir` (canonicalized; plus detected project root) is a first-class indexed field in the thread store. The store itself is global (`~/.agora/threads` + `~/.agora/state.db`, per agora-spec-persistence), never per-project — the wd is metadata, which is what makes cross-dir resume possible.
- **Resume defaults to the wd filter**: `/resume` lists sessions whose working_dir matches the current wd/project root. **Removing the filter** (`/resume --all`, picker toggle) shows everything; resuming a foreign session **switches both markers together** — the session becomes active AND the daemon's working dir switches to that session's wd. Session marker and wd marker move as one unit; the TUI footer always shows the active wd.
- Explicit wd change within a session (`/cd <dir>`) updates the session's wd marker (recorded, so a later resume restores the latest wd). One wd per session at a time; optional `add_dir` extra writable roots for multi-root work (codex `--add-dir` equivalent).
- **Dispatch pods**: pod home is fixed; `provision.workspace.dir` sets the session wd (checkout if missing). `session.resume` on a fresh pod restores the wd marker and re-establishes the workspace — wd-linked sessions are what make pod handoff coherent.

### Sandboxing policy (statement here; enforcement spec parked)

Default execution policy = **write limited to the working dir** (+ scratch/tmp + declared add_dirs), **read allowed everywhere** — codex's workspace-write profile, adopted. Refinements:
- Write outside wd ⇒ approval (escalation, not denial).
- Protected even inside wd: `.git` (approval for destructive ops), `.agora/`, `.cairn/` (the cairn VCS store — objects.git/cairn.db/wc.json must not be agent-writable except through cairn itself).
- Protected from *read*: the identity key store and credentials (`~/.agora/identity`, `.credentials.json`, keyring-backed material) — the agent must never read key bytes, read-everywhere notwithstanding.
- Network policy is orthogonal (per profile). Enforcement mechanism (bubblewrap port etc.) remains parked per the index; the *policy semantics* above are fixed now so approvals/hooks/execpolicy design against them.

## 4. Chain composition rules

- Every message/event carries `thread_id`/`turn_id` — a chain component can multiplex sessions.
- Backpressure: stdout writes block; a slow consumer slows event emission, never drops (except deltas, which are droppable by design).
- Exit codes (one-shot `agora exec` mode): 0 turn completed, 2 turn failed, 3 interrupted — script-chainable.
- Identity: pipe mode inherits the invoking user/session; when driven by the nexus broker, the dispatch envelope supplies the aspect identity — chainability is what makes agora dispatchable as a broker pod later (same channel, different transport).

## 5. Build note

This contract is **the first thing to freeze** — TUI, workflows progress, vessel, and dispatch all consume it. Ship pipe mode before the TUI; the TUI then proves the session protocol.
