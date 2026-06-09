# Agora Operator-Client Rework Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Each PHASE is one single-ticket PR.

**Goal:** Rework agora from an aspect-hosting TUI into a pure one-to-one operator-WS client for always-on agents (spec: `docs/2026-06-10-agora-operator-client-design.md`).

**Architecture:** Delete the in-process funnel/engine; add `internal/opclient` (operator WebSocket: RPC + pushes + reconnect); rewire the existing bubbletea UI to a single full-screen `dm:<agent>` thread fed by opclient. The broker side already has everything (verified — see Wire Facts).

**Tech Stack:** Go, bubbletea/lipgloss (existing `internal/ui`), `nhooyr.io/websocket` or `gorilla/websocket` — **use whatever `internal/bus/bus.go` already imports** (keep the dep), `github.com/CarriedWorldUniverse/nexus/nexus/frames` for the envelope.

---

## Wire Facts (verified 2026-06-10 against nexus main — do not re-derive, do confirm compiles)

- **WS endpoint:** `wss://<broker>/connect?token=<jwt>` (`comms.js wsURL`). Empty
  token under bypass: broker `auth()` resolves token-less as `{operator, Admin:true}`.
- **Auth-mode probe:** `GET /api/auth/mode` → `{"bypass":true|false}`. Bypass →
  connect with empty token. Non-bypass → operator passkey login is browser-only
  (`POST /api/operator/login/begin|finish`, WebAuthn): headless agora takes a
  pre-minted JWT via `-token`/`AGORA_TOKEN` and documents that; do NOT implement
  WebAuthn in the TUI.
- **Envelope** (`nexus/frames.Envelope`): `{kind, id, in_reply_to, ts, payload}`
  (+ optional `target_aspect`). RPC = send request kind with fresh `id`, await
  frame with kind `<request>.result` and matching `in_reply_to`/`id` correlation
  — mirror `comms.js rpc()` semantics exactly.
- **RPC kinds used:** `chat.list {after_id, limit}` → `{messages, has_more}`;
  `chat.send {content, topic, reply_to}` (fire-and-forget — the broker does NOT
  emit a `.result` for chat.send; `comms.js` line ~240 says so. Send without
  awaiting); `roster.list {}` → `{aspects:[{name,…,online/live}]}` (rows embed
  `schemas.AspectState` — confirm the online-flag field name in
  `nexus/broker/admin.go adminRosterAspect`/`schemas.AspectState` when coding);
  `runs.list {limit}` → `{runs}`.
- **Pushes (after `subscribe.chat` / `subscribe.observe`):** `chat.update`
  (payload = one chat message: `{id, from, content, reply_to, topic,
  received_at, …}` — `frames.ChatDeliverPayload` shape), `runs.update`.
- **Escalations (exists, no broker work):** aspects send `escalation.request`;
  the broker relays to ALL operator connections (`nexus/broker/escalation.go`);
  the operator answers with `escalation.decision` WITHOUT correlation envelope
  (the request id rides in the payload — read `escalation.go` lines 18-30 +
  agora's existing `internal/ui/escalation.go` for the exact payload fields;
  agora already has the decision-payload types from the in-process era — reuse).
- **DM convention:** topic `dm:<agent>`, content must include `@<agent>`
  (dashboard `sendDM`). A thread message belongs iff `msg.topic === "dm:<agent>"`.
  Replies inherit the thread topic broker-side (merged #339).

## File Map

| File | Role |
|---|---|
| `internal/opclient/client.go` (new) | dial, auth probe, envelope codec, rpc(), subscriptions, reconnect+cursor |
| `internal/opclient/client_test.go` (new) | httptest WS server: rpc round-trip, push delivery, reconnect catch-up |
| `internal/ui/*` (modify) | feed from opclient events; status line (presence/working/connection); keep blocks/input/escalation modal |
| `cmd/agora/main.go` (rewrite) | flags `-broker -agent [-token]`; opclient→ui wiring; engine gone |
| `internal/engine/`, `internal/bus/` (delete in Phase B) | replaced by opclient |
| `go.mod` (modify, Phase B) | drop bridle + funnel deps |

---

## PHASE A — `internal/opclient` (pure addition, PR 1)

### Task A1: envelope codec + dial + auth probe

**Files:** Create `internal/opclient/client.go`, `internal/opclient/client_test.go`

- [ ] **Step 1: failing test — auth-mode probe + dial with empty token**

```go
func TestDialBypass(t *testing.T) {
    srv := newFakeBroker(t) // httptest server: GET /api/auth/mode → {"bypass":true}; /connect upgrades WS
    defer srv.Close()

    c, err := opclient.Dial(context.Background(), opclient.Config{BrokerURL: srv.URL})
    if err != nil { t.Fatalf("Dial: %v", err) }
    defer c.Close()
    if got := srv.lastConnectToken(); got != "" {
        t.Fatalf("bypass dial sent token %q, want empty", got)
    }
}
```

`newFakeBroker` is the test rig you build once and every later test reuses: an
`httptest.Server` whose mux serves `/api/auth/mode` and upgrades `/connect`,
recording the `token` query param and exposing send/expect helpers for frames.

- [ ] **Step 2: run** `go test ./internal/opclient/ -run TestDialBypass` — FAIL (no package)
- [ ] **Step 3: minimal impl** — `Config{BrokerURL, Token, PinnedCertPEM}`,
  `Dial`: GET `/api/auth/mode`; if bypass and Token=="" connect token-less, else
  append `?token=`. Reuse the WS library and the pinned-cert TLS trust pattern
  from `internal/bus/bus.go` (copy the dialer TLS config; the cert pin now comes
  from Config not a keyfile).
- [ ] **Step 4: test passes**
- [ ] **Step 5: commit** `feat(opclient): dial + auth-mode probe`

### Task A2: rpc() + fire-and-forget send

- [ ] **Step 1: failing tests**

```go
func TestRPCRoundTrip(t *testing.T) {
    // fake broker answers chat.list with kind "chat.list.result", in_reply_to = req id,
    // payload {"messages":[{"id":7,"from":"maren","content":"hi","topic":"dm:maren"}],"has_more":false}
    msgs, _, err := c.ChatList(ctx, 0, 50)
    if err != nil || len(msgs) != 1 || msgs[0].From != "maren" { t.Fatalf("...") }
}
func TestChatSendNoResult(t *testing.T) {
    // ChatSend returns nil immediately; fake broker asserts it RECEIVED the frame
    // {kind:"chat.send", payload:{content:"@maren hi", topic:"dm:maren"}} but sends no result.
}
```

- [ ] **Step 2: run — FAIL**
- [ ] **Step 3: impl** — single reader goroutine demuxes frames: `in_reply_to`
  (or `.result` kind + id match, mirror comms.js) → pending-rpc map; otherwise →
  push channel. `rpc(kind, payload)` with a 10s deadline. Typed wrappers:
  `ChatList(afterID, limit)`, `ChatSend(content, topic, replyTo)` (no await),
  `RosterList()`, `RunsList(limit)`, `Subscribe(kinds...)`.
- [ ] **Step 4: pass** · **Step 5: commit** `feat(opclient): rpc + typed calls`

### Task A3: pushes + reconnect + cursor

- [ ] **Step 1: failing tests** — (a) after `Subscribe("subscribe.chat")`, a
  fake-broker `chat.update` frame arrives on `c.Events()` as `MsgEvent`; (b) kill
  the fake broker's conn, restart it: client reconnects (backoff floor 1s for the
  test via Config), re-subscribes, and calls `chat.list after_id=<last seen>` —
  fake broker asserts the catch-up call and the missed message surfaces on
  `Events()` exactly once.
- [ ] **Step 2: FAIL** · **Step 3: impl** — `Events() <-chan Event` where
  `Event = MsgEvent | RunEvent | EscalationEvent | ConnState`; reconnect loop
  with 1s..32s backoff; `lastSeenID` updated on every MsgEvent; catch-up dedup
  by message id. Persist `lastSeenID` to `~/.agora/cursor.json` (Config.StateDir
  override for tests) on update; load at Dial.
- [ ] **Step 4: pass** · **Step 5: commit** `feat(opclient): pushes, reconnect, cursor`
- [ ] **Step 6: PR** — `gh pr create` (Phase A is a pure addition; CI green; merge before Phase B).

## PHASE B — the flip: cmd + ui rewire, engine removal (PR 2)

### Task B1: cmd/agora on opclient

**Files:** Rewrite `cmd/agora/main.go`; keep `crash.go`/`recover.go` untouched.

- [ ] **Step 1:** flags: `-broker` (default `https://nexus.tail41686e.ts.net:7888`),
  `-agent` (REQUIRED — exit 2 with usage if empty), `-token` (default
  `os.Getenv("AGORA_TOKEN")`), `-state-dir` (default `~/.agora`). Delete
  `-keyfile -claude -cwd -cursor-dir` and all engine/funnel/bridle imports +
  construction. Wire: `opclient.Dial` → bubbletea program with the client +
  agent name in the model.
- [ ] **Step 2:** `go build ./...` — engine package still builds standalone (deleted in B3); cmd compiles without it.
- [ ] **Step 3: commit** `feat(cmd): agora dials the broker as an operator client`

### Task B2: ui rewire — single dm thread

**Files:** Modify `internal/ui/chat.go`, `messages.go`, `input.go`, `styles.go` (+ their tests).

- [ ] **Step 1: failing tests** — port the existing `chat_test.go` reducer
  patterns to the new msg source: (a) `MsgEvent{topic:"dm:maren"}` appends to the
  thread; (b) `MsgEvent{topic:"NEX-1"}` and `topic:""` are ignored; (c) own sends
  render optimistically then reconcile by id on the echo push; (d) status line
  states: connecting / online / offline / working (from `RunEvent`) render.
- [ ] **Step 2: FAIL** · **Step 3: impl** — the ui model holds `agent string`;
  `belongs(msg) = msg.Topic == "dm:"+agent`; history load on start
  (`ChatList` then filter, oldest-first — same as dashboard); composer submit →
  `ChatSend("@"+agent+" "+text, "dm:"+agent, replyTo)`. Keep the existing block
  rendering + fence handling untouched.
- [ ] **Step 4: pass** · **Step 5: commit** `feat(ui): one-to-one dm thread over opclient`

### Task B3: delete the engine

- [ ] **Step 1:** `git rm -r internal/engine internal/bus`; remove bridle +
  nexus/frame/funnel from go.mod (`go mod tidy`); fix any straggler imports.
  The escalation modal types in `internal/ui/escalation.go` STAY — if they
  imported engine/bus types, lift the needed payload structs into
  `internal/opclient/escalation.go` verbatim.
- [ ] **Step 2:** `go build ./... && go vet ./... && go test ./...` — green, no engine refs.
- [ ] **Step 3: commit** `refactor: remove in-process hosting (engine/bus)` · **PR 2**.

## PHASE C — escalations + live verify (PR 3)

### Task C1: escalation round-trip

- [ ] **Step 1: failing test** — fake broker pushes `escalation.request`
  (payload fields copied from `internal/ui/escalation.go`'s existing types);
  ui surfaces the modal; choosing approve emits `escalation.decision` with the
  request id in the payload (NO in_reply_to envelope correlation — per
  `nexus/broker/escalation.go` wire note). Fake broker asserts the frame.
- [ ] **Step 2: FAIL** · **Step 3: impl** — `EscalationEvent` already flows from
  A3's demux; wire modal show/decide into the model update; decision via
  `c.EscalationDecide(payload)`.
- [ ] **Step 4: pass** · **Step 5: commit** `feat: network escalation approvals`

### Task C2: live acceptance (operator-run, documented in the PR)

- [ ] `agora -agent maren` against the live broker: history renders; send a
  message; maren's reply arrives pushed, in-thread; status line shows working
  during the turn. Restart agora mid-conversation → instant resume from cursor.
  `kubectl rollout restart deployment/nexus-broker` mid-session → agora
  backoff-reconnects + catches up, conversation intact. Same thread visible in
  dashboard Converse. Paste the transcript/screens in the PR. **PR 3.**

---

## Self-review notes

- Spec coverage: one-to-one bind (B1 required flag, B2 belongs-filter), history
  (B2), live push (A3/B2), in-turn (A3 RunEvent + B2 status line), escalations
  (C1), reconnect+cursor (A3, C2 proves), auth bypass/token (A1), engine
  removal (B3), no broker changes (verified Wire Facts). Voice/status-page/multi-
  agent: out of scope per spec.
- The one deliberately-deferred confirm: the roster online-flag field name
  (`schemas.AspectState`) — A2's typed RosterList confirms it at compile time
  against the imported schema; flagged in Wire Facts, not invented here.
