# Agora One-to-One Conversation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the operator↔agent DM channel reliable end-to-end (dead connections detected in seconds, transcripts self-heal) and make agora feel like a live one-to-one TUI conversation (turn rhythm, clean transcript, on-demand trace pane).

**Architecture:** Spec at `docs/superpowers/specs/2026-06-10-agora-one-to-one-conversation-design.md`. Four workstreams: W1 in nexus (`~/Source/nexus`) — app-level ping frames + dashboard liveness watchdog; W2–W4 in agora (`~/Source/agora`) — opclient heartbeat, chat feel, trace pane. One PR per workstream.

**Tech Stack:** Go, coder/websocket, Bubble Tea (+ bubbles textarea/viewport), glamour (new dep, W3), vanilla JS dashboard (nexus `static/dashboard`).

**Root-cause context (from live diagnosis 2026-06-10):** little-blue's tailnet path to the broker goes black for 1–20+ min stretches (disco-key churn → DERP route loss). The broker's 30s server-side ping correctly reaps zombie conns (median operator conn lifetime ~5 min). Neither client detects this: agora's `readLoop` blocks forever in `conn.Read()` (no client ping, no read deadline → its reconnectLoop never wakes); the browser dashboard cannot send WS protocol pings and trusts a stale `readyState`. Additionally agora's `Subscribe` sends `{}` for `subscribe.observe` — the broker requires an `aspect` field and ignores the subscription (`observe.go:36`), so observe has never worked from agora.

---

## W1 — Client-detectable liveness (nexus repo, branch `fix/operator-ws-liveness`)

### Task 1: App-level ping/pong frame kind (broker)

Browsers cannot send WS protocol pings; give operator clients an app-level ping RPC.

**Files:**
- Modify: `nexus/frames/frames.go` (kind constants, ~line 180)
- Modify: `nexus/broker/operator_subs.go` (`dispatchOperatorSubFrame`)
- Test: `nexus/broker/operator_subs_test.go`

- [ ] **Step 1: Write the failing test** — in `operator_subs_test.go`, following the existing subscribe-ack test pattern (operator conn fixture, send frame, read response):

```go
func TestOperatorPingGetsPong(t *testing.T) {
	h := newOperatorHarness(t) // reuse the file's existing operator WS fixture helper
	resp := h.roundTrip(t, frames.Envelope{Kind: frames.KindPing, ID: "p1"})
	if resp.Kind != frames.KindPong {
		t.Fatalf("kind = %q, want %q", resp.Kind, frames.KindPong)
	}
	if resp.InReplyTo != "p1" {
		t.Fatalf("in_reply_to = %q, want p1", resp.InReplyTo)
	}
}
```

(Adapt fixture/helper names to what `operator_subs_test.go` actually defines — keep the assertion shape.)

- [ ] **Step 2: Run** `go test ./nexus/broker/ -run TestOperatorPingGetsPong` — expect FAIL (undefined `frames.KindPing`).

- [ ] **Step 3: Implement.** In `frames.go` next to the other kind constants:

```go
KindPing Kind = "ping"
KindPong Kind = "pong"
```

In `dispatchOperatorSubFrame`'s switch (it already returns true for handled kinds):

```go
case frames.KindPing:
	resp, _ := frames.NewResponse(frames.KindPong, env.ID, struct{}{})
	c.send(resp)
```

- [ ] **Step 4: Run the test** — expect PASS. Run `go test ./nexus/...` for no regressions.
- [ ] **Step 5: Commit** `feat(broker): app-level ping/pong for operator clients`

### Task 2: Dashboard liveness watchdog + reconnect refetch (nexus)

**Files:**
- Modify: `nexus/broker/static/dashboard/js/comms.js` (watchdog + reconnected event)
- Modify: `nexus/broker/static/dashboard/js/views/ConverseView.js` (refetch on reconnect)

- [ ] **Step 1: Watchdog in comms.js.** After `ws.onopen` (line ~116), start a 30s interval: send `{kind:"ping", id:<uuid>}` via the existing `sendFrame`; record `state.lastPong = Date.now()` whenever a `pong` frame arrives in `handleFrame`. If `Date.now() - state.lastPong > 75000` at ping time, call `ws.close()` — this fires the existing `onclose` → `scheduleReconnect()` path. Clear the interval in `onclose`. Initialize `state.lastPong` on open.

```js
// onopen tail:
state.lastPong = Date.now();
state.pingTimer = setInterval(() => {
  if (Date.now() - state.lastPong > 75000) { try { ws.close(); } catch {} return; }
  sendFrame({ kind: 'ping', id: crypto.randomUUID() });
}, 30000);
// handleFrame, before the subs dispatch:
if (env.kind === 'pong') { state.lastPong = Date.now(); return; }
// onclose head:
if (state.pingTimer) { clearInterval(state.pingTimer); state.pingTimer = null; }
```

- [ ] **Step 2: Reconnected event.** In `ws.onopen`, if this open is a reconnect (track `state.everConnected`), after replaying `state.subRequests` dispatch `window.dispatchEvent(new CustomEvent('comms:reconnected'))`. In `ConverseView.js`'s mount effect, add a `comms:reconnected` listener that calls the existing `loadMessages()`; remove it on unmount.

- [ ] **Step 3: Manual verification on dMon** (no JS test rig exists): deploy per the dMon redeploy steps, open dashboard, `sudo kubectl exec` a `tc`-free simulation instead — simplest: restart the broker pod and verify the dashboard recovers the conversation without a manual refresh; then verify in devtools that `ping` frames flow every 30s and a `pong` returns.

- [ ] **Step 4: Commit** `fix(dashboard): WS liveness watchdog + refetch on reconnect`

### Task 3: chat.deliver fan-out observability (broker)

One Debug log so push delivery is diagnosable (it was invisible during this investigation).

**Files:**
- Modify: `nexus/broker/operator_subs.go:176` (`broadcastChatDeliverToOperators`)

- [ ] **Step 1:** Count recipients in the predicate loop and log:

```go
func (b *Broker) broadcastChatDeliverToOperators(env frames.Envelope) {
	n := 0
	b.fanOutToOperators(env, func(c *wsConn) bool {
		if c.subscribedChat {
			n++
			return true
		}
		return false
	})
	b.log.Debug("chat.deliver operator fan-out", "subscribers", n)
}
```

- [ ] **Step 2:** `go test ./nexus/broker/` green; commit `chore(broker): log chat.deliver operator fan-out count`. Open the W1 PR.

---

## W2 — Agora connection health (agora repo, branch `fix/opclient-liveness`)

### Task 4: Client-side heartbeat in opclient

**Files:**
- Modify: `internal/opclient/client.go` (`connect()`, ~line 343)
- Test: `internal/opclient/client_test.go`

- [ ] **Step 1: Write the failing test.** A server that accepts the WS, answers the auth probe and register handshake (reuse the file's existing fake-broker fixture), then **stops reading frames entirely** — coder/websocket only answers protocol pings while the peer reads, so the client's `Ping` times out. Assert the client emits `ConnState{Connected: false}` and then redials (fixture sees a second connection) within a deadline:

```go
func TestHeartbeatDetectsDeadConnAndReconnects(t *testing.T) {
	srv := newFakeBroker(t, fakeBrokerOpts{stopReadingAfterConnect: true})
	c := dialTestClient(t, srv, Config{HeartbeatInterval: 100 * time.Millisecond, HeartbeatTimeout: 200 * time.Millisecond, ReconnectMin: 50 * time.Millisecond})
	waitForEvent(t, c, func(ev Event) bool {
		cs, ok := ev.(ConnState)
		return ok && !cs.Connected
	}, 5*time.Second)
	srv.expectRedial(t, 5*time.Second)
}
```

(Adapt to the existing fixture vocabulary in `client_test.go`; add `HeartbeatInterval`/`HeartbeatTimeout` to `Config` with defaults 20s/10s when zero.)

- [ ] **Step 2: Run it** — FAIL (client never notices; test times out on the ConnState wait).

- [ ] **Step 3: Implement.** In `connect()` after `go c.readLoop(conn, done)`, start a heartbeat goroutine bound to this conn:

```go
go c.heartbeat(conn, done)

func (c *Client) heartbeat(conn *websocket.Conn, done <-chan error) {
	t := time.NewTicker(c.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, c.cfg.HeartbeatTimeout)
			err := conn.Ping(ctx)
			cancel()
			if err != nil {
				// Read in readLoop returns once we close; that path owns
				// ConnState emission and reconnect wake-up.
				_ = conn.Close(websocket.StatusGoingAway, "heartbeat failed")
				return
			}
		}
	}
}
```

Goroutine lifetime: it selects on `c.ctx.Done()` and otherwise exits on the first `Ping` failure — pinging a conn that readLoop already closed errors immediately, so a normal disconnect also ends the goroutine within one tick. No extra signalling channel needed; drop the `done` parameter.

- [ ] **Step 4: Run** `go test ./internal/opclient/ -run TestHeartbeat` — PASS; full `go test ./...` green.
- [ ] **Step 5: Commit** `fix(opclient): client-side heartbeat detects half-open connections`

### Task 5: Reconnect visible in the transcript + status line states

**Files:**
- Modify: `internal/ui/blocks.go` (`applyOpEvent` ConnState case, `renderStatus`)
- Test: `internal/ui/blocks_test.go`

- [ ] **Step 1: Failing tests:** (a) ConnState false → status renders `reconnecting…`; (b) a disconnect/reconnect cycle appends system lines to the transcript ("— connection lost", "— reconnected"); (c) ConnState true after false → `online`. Use the file's existing Model fixture + `applyOpEvent` directly.

- [ ] **Step 2: Implement.** In the ConnState case: on transition online→offline append a system chat block `"— connection lost; reconnecting…"`; on offline→online append `"— reconnected"`. Status line states: `online` / `reconnecting…` (the client always retries, so there is no terminal "offline"). System blocks reuse the existing block machinery with a dim style.

- [ ] **Step 3:** Tests PASS; commit `feat(ui): connection state in status line + transcript markers`. Open the W2 PR.

---

## W3 — Chat feel (agora repo, branch `feat/chat-feel`)

### Task 6: Fix subscribe.observe payload (prerequisite for presence + W4)

**Files:**
- Modify: `internal/opclient/client.go` (`Subscribe`, line 239)
- Modify: `internal/ui/model.go` (subscribeCmd, ~line 279)
- Test: `internal/opclient/client_test.go`

- [ ] **Step 1: Failing test:** fake broker records subscription frames; assert `subscribe.observe` carries `{"aspect":"<agent>"}` and replay-after-reconnect re-sends the same payload.
- [ ] **Step 2: Implement** a payload-aware variant and use it from the UI:

```go
// SubscribeWith stores+sends one subscription with an explicit payload,
// replayed verbatim on reconnect.
func (c *Client) SubscribeWith(ctx context.Context, kind string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.subMu.Lock()
	c.subscriptions[kind] = raw
	c.subMu.Unlock()
	return c.sendRaw(ctx, kind, raw)
}
```

In `model.go` subscribeCmd: `c.Subscribe(ctx, "subscribe.chat")` + `c.SubscribeWith(ctx, "subscribe.observe", map[string]string{"aspect": cfg.Agent})`. (Check the exact payload field name against `nexus/broker/observe.go`'s decode struct before coding — it is `aspect` per the warn log.)

- [ ] **Step 3:** PASS + commit `fix(opclient): subscribe.observe must carry the aspect`

### Task 7: Observe events in opclient

**Files:**
- Modify: `internal/opclient/client.go` (`demux`)
- Test: `internal/opclient/client_test.go`

- [ ] **Step 1: Failing test:** inject an `observe.frame` envelope through the fake broker; assert an `ObserveEvent` arrives on `c.Events()` with aspect/kind/summary populated. Mirror the payload shape from `nexus/broker/observe.go` `sendObserveFrame` (read it first; fields include the aspect, a seq, and the begin/event/end frame body).
- [ ] **Step 2: Implement:**

```go
type ObserveEvent struct {
	Aspect string
	Seq    int64
	Kind   string          // "begin" | "event" | "end"
	Body   json.RawMessage // frame body for the trace pane to format
}
```

demux case `"observe.frame"`: decode, `c.emit(ObserveEvent{...})`.

- [ ] **Step 3:** PASS + commit `feat(opclient): surface observe frames as events`

### Task 8: Turn rhythm — local echo, ✓ ack, presence line

**Files:**
- Modify: `internal/ui/blocks.go`, `internal/ui/model.go`, `internal/ui/chat.go`
- Test: `internal/ui/chat_test.go`

The ack is our own `chat.deliver` echo: the broker broadcasts every persisted message to **all** subscribed operators including the sender (`operator_subs.go:176` has no sender exclusion). Verify once against a live broker before relying on it; if the echo is absent, fall back to marking ✓ on `chat.send` write success.

- [ ] **Step 1: Failing tests:** (a) sending appends a pending block (`…` marker) immediately; (b) the matching `chat.deliver` echo (same content, `from=operator`, topic `dm:<agent>`) flips it to ✓ with the server timestamp and records the id in `seen` (no duplicate block); (c) `ObserveEvent{Kind:"begin"}` for the agent sets a presence flag rendered as `"<agent> is working… <elapsed>"`; (d) presence clears on `end`, on the agent's reply arriving, and on a 5-minute-since-last-observe-frame timeout (drive a fake clock through the model rather than sleeping); (e) strict 1:1 — a `chat.deliver` with topic `""`, `general`, or another agent's DM is not appended to the transcript (`belongs()` already filters; lock it in with tests).
- [ ] **Step 2: Implement.** Pending-echo matching: queue of unacked sends matched FIFO against operator-authored deliveries in the DM topic. Presence: `m.presence{active bool, since, lastFrame time.Time}` set by ObserveEvent begin/event (any frame refreshes `lastFrame`), cleared by end/reply/timeout; a 1s `tea.Tick` while active re-renders the elapsed counter and checks the timeout. Replace the `RunEvent`-driven `m.working` (runs.* is dispatch Jobs, never DM turns — keep RunEvent handling but stop rendering it as presence).
- [ ] **Step 3:** PASS + commit `feat(ui): turn rhythm — local echo, delivery ack, observe-driven presence`

### Task 9: Transcript styling + markdown

**Files:**
- Modify: `internal/ui/blocks.go`, `internal/ui/styles.go`, `go.mod` (add `github.com/charmbracelet/glamour`)
- Test: `internal/ui/blocks_test.go`

- [ ] **Step 1: Failing tests:** rendered transcript shows (a) speaker header lines styled distinctly for operator vs agent with `HH:MM` timestamps; (b) blank line between consecutive speaker turns; (c) agent markdown (`**bold**`, fenced code) rendered via glamour (assert on glamour output markers, not raw asterisks).
- [ ] **Step 2: Implement:** speaker header = lipgloss-styled name + dim timestamp; agent message bodies pass through a glamour `TermRenderer` (`glamour.WithAutoStyle()`, width = viewport width); operator messages stay plain. Construct the renderer once per resize, not per message.
- [ ] **Step 3:** PASS + commit `feat(ui): conversation transcript styling + markdown rendering`

### Task 10: Input ergonomics audit + scroll hold

**Files:**
- Modify: `internal/ui/input.go`, `internal/ui/model.go`
- Test: `internal/ui/input_test.go`

Textarea + input-history already exist (`input.go`); `m.unreadBelow` exists. This task closes the gaps, not rebuilds.

- [ ] **Step 1: Failing tests:** (a) multiline compose is on by default (`textareaEnabled` true without a flag); (b) when scrolled up, an arriving message does NOT move the viewport and increments the jump-to-bottom indicator (`N new ↓` rendered); (c) pressing `end`/`G` jumps to bottom and clears it.
- [ ] **Step 2: Implement** whichever of those fail (audit first — some may already pass; delete the redundant tests if so and note it in the PR).
- [ ] **Step 3:** PASS + commit `feat(ui): default multiline compose + scroll hold with unread indicator`. Open the W3 PR.

---

## W4 — Trace pane (agora repo, branch `feat/trace-pane`)

### Task 11: Turn-scoped ring buffer

**Files:**
- Create: `internal/ui/trace.go`
- Test: `internal/ui/trace_test.go`

- [ ] **Step 1: Failing tests:** feed ObserveEvents (begin, 3 events, end) × 5 turns into a `traceLog`; assert it retains only the most recent 3 turns; assert `lines()` formats one compact line per event (`HH:MM:SS ▸ <kind summary>`) with begin/end delimiters.
- [ ] **Step 2: Implement** `traceLog{turns []traceTurn}` (capacity 3 turns, a turn = begin..end or an unterminated tail); formatting helper renders from the `Body` raw JSON best-effort (tool name + first ~60 chars of detail; unknown shapes render the kind only — observe is diagnostics, lossy is fine).
- [ ] **Step 3:** PASS + commit `feat(ui): observe trace ring buffer`

### Task 12: Toggleable trace view

**Files:**
- Modify: `internal/ui/model.go`, `internal/ui/blocks.go`, `internal/ui/commands.go`
- Test: `internal/ui/blocks_test.go`

- [ ] **Step 1: Failing tests:** (a) `ctrl+t` swaps the viewport content from transcript to trace and back; (b) trace mode renders the ring buffer lines + a `trace — <agent> (ctrl+t to return)` header; (c) chat keystrokes (typing, send) are disabled in trace mode except scroll + `ctrl+t` + quit.
- [ ] **Step 2: Implement** as a full-viewport swap (`m.view = chat|trace`) — simplest layout that fits any terminal; live ObserveEvents keep appending while hidden (the buffer is fed in `applyOpEvent` regardless of mode).
- [ ] **Step 3:** PASS + commit `feat(ui): on-demand trace pane (ctrl+t)`. Open the W4 PR.

---

## Acceptance (live, on dMon)

1. agora + dashboard side by side; message sent in one appears live in the other, no refresh.
2. `sudo kubectl rollout restart deployment/nexus-broker -n nexus` mid-session → agora shows "reconnecting…" then "— reconnected", transcript gap-free; dashboard recovers without refresh.
3. Put the laptop to sleep 2 minutes, wake → both clients recover within ~75s worst-case.
4. DM cloud-shadow; presence line appears while it works, reply lands, `ctrl+t` shows the turn's tool trace.

## Sequencing & tickets

W1 → W2 → W3 → W4, one PR each, CI green before review, squash-merge. File NEX tickets per workstream and move them as work lands (W1 in nexus, W2–W4 in agora).
