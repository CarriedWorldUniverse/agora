# Agora as a true one-to-one TUI conversation

**Status:** spec (approved direction: approach A — harden + layer the chat feel onto the operator-client architecture)
**Date:** 2026-06-10
**Driver:** shadow (with the operator)

## Problem

The operator-client rework (NEX-549, PRs #22–#29) gave agora the right shape:
a full-screen DM thread with one always-on agent over the operator WS. Living
in it daily exposes three classes of problems:

1. **Comms reliability.** The connection silently dies — no error, no visible
   reconnect; the operator notices only from dead air. The dashboard shows the
   same root symptom (new messages need a manual refresh), so `chat.update`
   push delivery to operator connections is broken at the broker, not just in
   agora.
2. **Conversation flow feel.** Sending a message gives no acknowledgement,
   the agent's working time is silent dead air, and replies land as
   unceremonious lumps. It does not feel like a live 1:1 conversation.
3. **No observability of agent actions.** While the agent works there is no
   way to see what it is doing — not even on demand.

## Goals

- **Trustworthy delivery.** A message sent in agora appears live in the
  dashboard and vice versa, with no refresh. A dead connection is detected in
  seconds, shown, and self-heals with no transcript gaps.
- **Turn rhythm.** Local echo on send → ✓ on broker ack → live
  "`shadow` is working… 12s" presence while the agent deliberates → reply
  lands. No dead air, no wondering if it heard you.
- **A conversation, not a log.** Distinct speaker styling, timestamps, proper
  markdown, breathing room. Strictly the `dm:<agent>` thread — no cross-topic
  or system noise.
- **Quiet-by-default observability.** A keybinding toggles a trace pane
  showing the agent's turn activity (tool calls, snippets) live or after the
  fact. Closed, agora stays a clean chat.

## Non-goals

- Streaming the agent's prose into the chat as it generates (the operator
  chose final-reply-plus-on-demand-trace over a full live-trace chat).
- Multi-agent / multi-thread browsing (dashboard's job; one invocation = one
  agent stands).
- New broker protocol surfaces beyond heartbeat — presence and trace ride the
  existing `observe.*` stream and `chat.*` acks.
- Richer orchestrator status (separate status-page workstream).

## Design decisions (from brainstorming)

- **Presence source is `observe.begin`/`observe.end`, not `runs.update`.**
  `runs.*` tracks dispatch Jobs (tickets/repos/PRs) and never fires for a DM
  deliberation turn. The funnel already emits `observe.begin/event/end` over
  the aspect WS (`runtime/obsforward`), relayed by the broker to subscribed
  operators (`nexus/broker/observe.go`). One `subscribe.observe` serves both
  the presence line and the trace pane.
- **Observe frames are best-effort by design** (obsforward sends best-effort
  so a stalled WS can't wedge a turn). Presence must therefore be tolerant:
  the reply's `chat.update` arriving always clears "working…", and a stale
  presence line times out rather than lying forever.
- **The broker delivery bug is fixed first.** Feel-polish on a lossy channel
  is wasted work; W1 lands before W2–W4 are judged.

## Workstreams

### W1 — Broker push reliability (nexus repo)

The root reliability bug, shared by agora and the dashboard.

- **Diagnose and fix operator push delivery.** `chat.update` pushes are not
  reliably reaching operator connections. Suspects: the operator-subscription
  lifecycle on the pod broker, or `wsConn.send()` failing silently on
  half-dead connections. Fix at the broker so every operator surface heals.
- **Heartbeat.** The broker pings operator WSes on an interval and reaps
  connections that miss pongs; opclient pings client-side likewise. A dead
  connection becomes a *detected event* within seconds on both ends instead
  of silent dead air.

**Acceptance:** message sent in agora appears live in the dashboard (and vice
versa) with no refresh; killing the broker pod mid-session makes agora
visibly reconnect and catch up with zero transcript gaps.

### W2 — Connection health in agora

- `internal/opclient` grows an explicit connection-state machine
  (connected / reconnecting / offline) surfaced as events to the UI.
- The UI shows a persistent one-line connection status in the chrome.
- Auto-reconnect with backoff is made provably correct: tests kill the WS
  mid-session and assert recovery; on reconnect, `chat.list after_id=<cursor>`
  heals the transcript.

### W3 — Chat feel in agora

- **Turn rhythm:** local echo immediately on send; ✓ when the broker acks
  `chat.send`; presence line "`<agent>` is working… `<elapsed>`" driven by
  `observe.begin` with a live timer; cleared by `observe.end`, by the reply's
  `chat.update`, or by timeout (whichever first). The timeout is measured
  from the *last observe frame seen for the turn* (so long turns that keep
  emitting events keep their presence line), configurable, default 5 minutes.
- **Transcript:** distinct speaker styling, timestamps, markdown rendering,
  spacing between turns.
- **Strict 1:1:** only `dm:<agent>` messages reach the model layer; all other
  traffic on the subscription is dropped at the opclient/UI boundary.
- **Input:** multiline compose, input-history recall (up-arrow), scrollback
  that holds position while new messages arrive, with a jump-to-bottom
  indicator when scrolled up.

### W4 — On-demand trace pane

- A keybinding toggles a trace pane (e.g. split or overlay — plan-time
  choice within Bubble Tea layout constraints).
- Agora maintains a `subscribe.observe` for its agent; `observe.event`s
  render as compact one-liners (tool name + summary), `begin`/`end` delimit
  turns.
- Events are ring-buffered in memory for the last few turns so the pane can
  be opened *after* a turn and still show what happened.
- Pane closed = today's quiet chat; pane open = the claude-code-ish working
  view, on the operator's terms.

## Edge cases

- **Reconnect mid-turn:** presence is re-derived (reply arrival / timeout),
  never stuck from a missed `observe.end`.
- **Broker restart:** cursor catch-up restores the transcript; presence and
  trace state reset cleanly.
- **Dropped observe frames:** presence timeout covers a lost `end`; the trace
  pane shows what arrived (best-effort is acceptable for diagnostics).
- **Cross-surface conversation:** messages sent from the dashboard while
  agora is open arrive as normal `chat.update`s and render in place.

## Testing

- Unit: opclient connection-state machine, presence-line state transitions
  (begin/end/reply/timeout orderings), dm-filter, ring buffer.
- E2E: harness against a real broker (cutover-smoke pattern) for
  kill-and-recover — broker pod restart, WS drop mid-turn, catch-up
  correctness.
- W1 acceptance is verified live on dMon (agora + dashboard side by side).

## Sequencing

1. **W1** — nexus ticket, lands first.
2. **W2, W3, W4** — independent agora tickets once W1 is in; fan out across
   builders per the one-ticket-per-builder dispatch policy.
