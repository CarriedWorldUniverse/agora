# Agora rework: dedicated operator TUI for always-on agents

**Status:** spec (approved direction: approach A — operator-WS client)
**Date:** 2026-06-10
**Driver:** shadow (with the operator)

## Problem

Agora today *hosts* an aspect: it takes an aspect keyfile and runs the funnel +
claude-code provider in-process (`internal/engine`), with the TUI as a window
into that local agent. Two structural consequences:

- The agent's presence is tied to the operator's machine — laptop sleeps,
  aspect gone.
- The TUI can only ever talk to the one agent it hosts.

The platform has moved: aspects run as always-on pods on the funnel (keel,
maren; cloud-shadow next, running one consistent session). What the operator
needs from a terminal is no longer a host — it's a **client**: a live 1:1
conversation with any online agent, from anywhere the broker is reachable.

## Goals (v1)

- Agora connects to the broker as the **operator identity** over the existing
  operator WebSocket — the same protocol the dashboard speaks. No new
  protocol; the TUI, dashboard Converse, and vessel are windows onto the same
  threads.
- Agent list with **online presence**; pick an agent → 1:1 DM thread.
- DM threads use the dashboard's `dm:<agent>` topic convention, so a
  conversation started in the TUI continues seamlessly in the dashboard and
  vice versa.
- **History replay** on open (`chat.list` + filter, as the dashboard does),
  live pushed deliveries (`subscribe.chat` → `chat.update`), reply threading
  (`reply_to`).
- A minimal **in-turn indicator** ("maren is working…") derived from the push
  stream available today; the richer reasoning-trace presence arrives with the
  status-page work, not here.
- **Escalation approvals** in the TUI, for *network* escalations
  (aspect → broker → operator), replacing the current in-process-only modal.
- Reconnect with backoff + cursor-based catch-up; never lose the thread on a
  broker restart.

## Non-goals (v1)

- Hosting an aspect (the entire point of the rework is removing this).
- Team/topic channel browsing (the dashboard does this well; the TUI is the
  1:1 surface — a later v-next can add channels if terminal life wants them).
- Voice (vessel's job).
- The orchestrator status page (separate workstream; the TUI will consume its
  stream later for richer presence).
- Multi-operator auth (single operator today).

## Architecture

```
┌──────────── little-blue / anywhere ───────────┐      ┌────────── k3s ──────────┐
│  agora (TUI)                                  │  WS  │  broker                 │
│  ├── internal/ui        (bubbletea, kept)     │◄────►│  /api/dashboard WS      │
│  ├── internal/opclient  (NEW: operator WS)    │      │  operator frames        │
│  └── cmd/agora          (flags reworked)      │      │   chat.* runs.* esc.*   │
│                                               │      └───────────┬─────────────┘
│  REMOVED: internal/engine (funnel hosting),   │                  │ funnel WS
│  bridle/claudecode dep, aspect keyfile        │       always-on aspect pods
└───────────────────────────────────────────────┘       (keel, maren, shadow…)
```

Agora becomes ~half its current size: the deliberation engine, provider
wiring, and aspect-keyfile handling are deleted; `internal/ui` (chat panel,
input, blocks, escalation modal) survives with a new message source;
`internal/bus` is replaced by `internal/opclient`.

## Components

### `internal/opclient` (new)

The operator-WS client. Mirrors the dashboard's `comms.js`/`api.js` contract:

- **Connection:** WebSocket to the broker's operator endpoint, JWT in the URL
  (`wsURL(jwt)` equivalent). TLS with the pinned broker cert (reuse the
  keyfile-style pin trust the repo already has — NEX-367 — pointed at an
  operator credential file instead of an aspect keyfile).
- **Frames:** `{kind, id, ts, payload}` JSON; request/response correlate by
  `id`; pushes arrive uncorrelated.
- **RPCs used:** `chat.list` (history, `after_id` paging), `chat.send`
  (content, `topic`, `reply_to` — the dashboard delegates all sends here, per
  its own `sendMessage` comment; agora does the same), `runs.list`/`run.get`
  (in-turn indicator), roster (online presence — same call the dashboard's
  Team panel uses; confirm exact kind at plan time against `comms.js`).
- **Subscriptions:** `subscribe.chat` → `chat.update` pushes; `runs.update`
  pushes for the in-turn indicator; the escalation push (see below).
- **Reconnect:** exponential backoff; on re-register, `chat.list after_id=
  <last seen id>` per open thread to close the gap. The cursor is in-memory
  per session plus a small state file (`~/.agora/cursor.json`) so reopening
  the app resumes the conversation view instantly.

### `internal/ui` (kept, rewired)

- Conversation list pane: online agents (presence dot), `★ shadow` pinned
  first — the same ordering convention as the dashboard's mobile Converse.
- Thread pane: existing message blocks/rendering (markdown fences already
  handled), `dm:<agent>` filtering identical to the dashboard's
  `messageBelongsToChannel` (topic `dm:<agent>`; with the reply-topic
  inheritance fix on the broker, replies stay in-thread).
- Composer: send → `chat.send {topic: "dm:<agent>", content: "@<agent> …"}`
  exactly as the dashboard's `sendDM` composes it (mention ensured in
  content); `reply_to` threading on selection.
- In-turn indicator: agent's row + thread header show "working…" while a run
  or turn for that aspect is active (from `runs.update`; coarse is fine).
- Escalation modal: kept visually; rewired to the operator escalation frames.

### Escalations (the one broker-side verify/extend)

Today the modal answers the *in-process* funnel. Network escalations flow
aspect → broker (`escalation.go`) → operator. **Plan-time verification:** what
the broker currently emits to the operator surface for an escalation
(panel-route notify exists), and whether an operator→broker *decision* RPC
exists. If the decision RPC is missing on the operator WS, add it broker-side
(small: forward decision with `InReplyTo` to the waiting aspect — the
aspect-side plumbing already exists). The TUI then: escalation push → modal →
approve/deny → decision RPC.

### `cmd/agora` (flags reworked)

```
agora -broker https://nexus.tail41686e.ts.net:7888 \
      [-cred ~/.agora/operator.json]   # operator JWT/token + pinned cert
      [-agent shadow]                  # open straight into a thread
```

Gone: `-keyfile` (aspect), `-claude`, `-cwd`, funnel/provider config. The
crash-recovery wrapper (`crash.go`/`recover.go`) stays — it's about terminal
hygiene, not hosting.

### Auth

Operator JWT/bypass exactly as the dashboard today (the broker mints the
dashboard's JWT; agora gets the same shape via `-cred` or an env var).
Herald-issued operator tokens replace this later — the credential is isolated
in `-cred`/env so the swap is one loader.

## Data flow (happy path)

1. `agora -agent maren` → opclient dials, authenticates, `subscribe.chat`,
   roster fetch → UI shows maren online.
2. `chat.list after_id=cursor` → thread pane renders history (topic-filtered
   to `dm:maren`).
3. Operator types → `chat.send {topic:"dm:maren", content:"@maren …"}`.
4. Broker delivers to maren's pod; maren's funnel turn runs (TUI shows
   "working…" from the push stream); reply posts with the inherited topic →
   `chat.update` push → thread renders it.
5. Broker restarts → opclient backoff-reconnects, re-subscribes, `chat.list
   after_id` catch-up; the conversation never visibly breaks.

## Error handling

- WS drop: backoff (1s..32s cap), status line shows offline state; queued
  outbound sends are held and flushed on reconnect (the dashboard's outbox
  pattern).
- RPC timeout: per-request deadline; failed sends marked in the UI with retry.
- Auth rejection: clear exit message pointing at `-cred` (no silent retry
  loop — the 401-monitor lesson from the current `main.go` carries over).

## Testing

- Unit: frame codec (marshal/unmarshal of every kind used), reducer-level UI
  state (message insert/ordering/topic filter — port the existing
  `chat_test.go` patterns), reconnect/cursor logic with a fake server.
- E2E (the proving step): against the live dMon broker — open a `dm:maren`
  thread from the TUI, converse with always-on maren (and keel), confirm the
  same thread renders in dashboard Converse. This is the acceptance test:
  **a live terminal conversation with an always-on agent, surviving an agora
  restart and a broker restart.**
- Escalation: forced escalation from a test aspect → modal → decision lands.

## Sequencing

This is **step 1** of the cloud-shadow arc: it ships value immediately
(terminal client for keel/maren) and is the surface cloud-shadow lands behind.
Step 2 (separate spec): the always-on shadow pod — one consistent session,
claude-code provider, home/memory continuity. Step 3: presence/status-page
stream consumed here for rich in-turn presence.

## Risks / open points

- **Escalation decision RPC** may need the small broker addition (flagged
  above) — verify first at plan time.
- **Roster/presence kind**: confirm the exact operator-WS call the dashboard
  uses for the online roster at plan time; do not invent.
- The operator JWT shape for a non-browser client (today the dashboard mints
  it in-page under bypass): confirm how a headless client obtains it — likely
  the same login/bypass endpoint the dashboard hits, automated in opclient.
