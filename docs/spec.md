# agora — interactive operator-facing CLI for the nexus cluster

**Date:** 2026-05-14
**Status:** v0.1 draft — fixes the architectural picture; TUI library committed to bubbletea provisionally
**Owner:** shadow
**Tracks:** NEX-46 (epic), NEX-48..NEX-59 (decomposed stories) in https://carriedworlduniverse.atlassian.net
**Companion to:** [`nexus/docs/2026-04-25-nexus-transport-spec.md`](https://github.com/CarriedWorldUniverse/nexus/blob/main/docs/2026-04-25-nexus-transport-spec.md) (v0.2 §13 deploy plane)
**Approvals on the architecture:** keel (chat msg_id=898, 905, 909), plumb (msg_id=899, 906, 910)

---

## 1. Problem

The operator wants to manage the nexus cluster through their operator-facing aspect. Today that aspect (shadow) runs as a claude-code session connected to nexus through a polling MCP (`nexus-comms-mcp`), with a 15-minute cron poll. Cross-aspect chat threads move forward in seconds; the coordinator runs minutes behind. Operator-with-15-min-lag isn't workable for coordination.

The fix is not poll-cadence tuning. It is to give operator-attended aspects the same WS-persistent operating point every other agentfunnel-driven aspect already has, plus an interactive terminal surface so the operator can participate in chat as the aspect — and have the aspect proactively surface cluster context to them.

agora is that surface.

## 2. Goals and non-goals

**Goals:**
- Push-delivery `chat.deliver` for the aspect; no more polling.
- Interactive TUI: operator types messages into the aspect's inbox, sees replies in real time, sees ambient bus traffic the aspect participates in.
- Proactive notification channel: the aspect can push private (operator-only) info into the TUI when something on the bus is load-bearing for the operator.
- Strict separation of two output channels: bus chat (sees everything) vs operator TUI (private side-channel).
- Same per-turn engine every other aspect uses (claude-code subprocess via bridle), so we inherit slash commands, MCP servers, native tool surface, Task/subagent without re-implementing.
- Outpost-managed lifecycle per the transport spec v0.2 §13.

**Non-goals (explicitly out of scope for v0):**
- "Cheap-turn" optimization where the outer shell handles trivial acks without spawning claude-headless. Two agents under one identity → drift. Parked behind the §4 invariant unless a clear state-divergence story lands.
- Native slash-command parser in the outer shell. Same drift class.
- Multi-aspect identity (one operator logged into multiple aspects via tabs).
- Markdown rendering polish, syntax highlighting in code blocks, file picker for attachments. v0.1+ polish.
- Crash supervision (Outpost handles, per transport spec §7.3).
- Replacing claude-code. claude-code stays for code work; agora is for chat-driven coordination. Side-by-side.

## 3. Architecture

```
┌──────────────────────────────────────────────────────┐
│  agora  (persistent process per aspect)              │
│                                                       │
│  ┌────────────────────────────────────────────────┐  │
│  │  Outer shell (persistent, we own)              │  │
│  │  - WS to nexus (chat.deliver pushes land here) │  │
│  │  - FIFO inbox (source-tagged: chat | tty)      │  │
│  │  - TUI (bubbletea: panel + prompt + status)    │  │
│  │  - REPL history, push-target registrations     │  │
│  │  - notify-operator render channel               │  │
│  │                                                 │  │
│  │           ↓ per turn: spawn engine             │  │
│  │                                                 │  │
│  │  ┌──────────────────────────────────────────┐  │  │
│  │  │  Per-turn engine                          │  │  │
│  │  │  bridle.Harness + claudecode provider     │  │  │
│  │  │  → spawns `claude --resume <sid> -p ...`  │  │  │
│  │  │  inherits: slash commands, MCP servers,   │  │  │
│  │  │  native tools, Task subagent              │  │  │
│  │  └──────────────────────────────────────────┘  │  │
│  │           ↑ FinalText + tool calls return      │  │
│  │                                                 │  │
│  │  Output routing (source-tag + tool calls):     │  │
│  │  - chat-source → send_chat → nexus bus         │  │
│  │  - tty-source  → render in TUI                 │  │
│  │  - notify_operator (tool) → TUI, never bus     │  │
│  │  - send_chat (tool) → bus, regardless of source│  │
│  └────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

### 3.1 Same machinery as autonomous agentfunnel

Below the TUI is reused, not reinvented. The funnel, the WS client (`runtime/aspect/wsasp` in nexus), the bridle harness, the claudecode provider, the keyfile auth (`runtime/keyfile`), the FIFO inbox (per #224), the per-thread session id (per #226) — all carry over. agora is a presentation surface over the existing engine.

agora depends on:

- `github.com/CarriedWorldUniverse/bridle` — per-turn engine.
- `github.com/CarriedWorldUniverse/nexus/runtime/wsasp` — WS client + Bridge.
- `github.com/CarriedWorldUniverse/nexus/runtime/keyfile` — keyfile loader, validation.
- `github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel` — FIFO funnel, ContextMode, filter, tool-runner composition.
- `github.com/charmbracelet/bubbletea` — TUI framework.

### 3.2 Per-turn startup cost is parity with the cluster

Every other agentfunnel-driven aspect already pays the cost of spawning `claude` per turn. Shadow moving to this shape is parity, not a regression. The "I have my own session that doesn't need to boot" advantage we currently have is the *same property* that produces the 15-min comms lag. Trading one for the other is the deal.

## 4. State boundary INVARIANT

Per plumb (chat msg_id=906): the boundary between outer shell and per-turn engine is a SPEC INVARIANT, not an implementation note. Enforced by structure. Any v0.1+ proposal that would violate it MUST also include a clear story for state divergence ("does the engine know about this on the next turn") before it ships.

### 4.1 Crosses outer shell → engine on each turn

- Session id (`bridle.SessionHandle.ID`), determined by `funnel.ContextMode` (per #226.4):
  - `ContextGlobal`: one session per process lifetime, rotated on compaction.
  - `ContextThreadIsolated`: deterministic uuid_v5 keyed on `(aspect_id, thread_root_msg_id)`.
- Session jsonl tail (the funnel's `SessionTail` — passed to claudecode via `--resume`).
- Popped inbox item — exactly one per turn (FIFO head pop, #224). Carries:
  - `bridle.InboxItem.From`, `Content`, `ReplyTo`, `ThreadRoot`, `MsgID` for chat-source items.
  - Synthetic `From` (operator name from keyfile) + `Content` + `Source: "tty"` + zero fields elsewhere for tty-source items.
- Ambient context: AspectID, keyfile-derived ProviderEnv (per #218), AspectHome, model, provider config.
- `TurnRequest.Tools` (in-process tool defs, including `notify_operator`) and `MCP` config — same as autonomous agentfunnel except `notify_operator` is only registered in TTY mode.

### 4.2 Stays in shell process-lifetime

- WS connection to nexus / outpost.
- FIFO inbox tail (items not yet processed by Deliberate).
- REPL history (operator's typed messages, last N).
- Push-target registrations (per transport spec §13.5 — TTY-mode aspects typically don't need them since they hold WS, but the option is there).
- TUI state: scrollback buffer, cursor position, scroll-pin state, layout (panel sizes), status-line fields.
- notify-operator render channel (in-process channel, no IPC).

### 4.3 Out of scope (must not exist as a code path until designed deliberately)

- Engine mutating shell state mid-turn (e.g., engine writing to TUI scrollback directly).
- Shell intercepting engine output before it lands in the TUI (e.g., shell post-processing engine text).
- Shell making any judgment about turn content beyond "render this token in the streaming line."
- Slash-command parsing in the shell that the engine doesn't see.
- "Cheap-turn" short-circuit (no shell-side ack-without-spawning-engine).

If a v0.1+ proposal needs to cross this boundary, it MUST land a state-divergence story alongside. Without that story, the v0 invariant holds.

## 5. TUI library: bubbletea

Committing to `github.com/charmbracelet/bubbletea` for v0. Rationale:

- **Event-driven model fits chat UI naturally** — Elm-style model/update/view. Incoming WS frame, key press, streaming token, resize — all just messages handed to the same Update loop.
- **Maturity and platform coverage** — charm-bracelet ecosystem (glow, gum, soft-serve) is well-maintained; works on macOS, Linux, Windows (ConPTY caveats handled by underlying x/term).
- **Composable ecosystem** — `lipgloss` (styling), `bubbles` (text-area, viewport, spinner), `glamour` (markdown render for v0.1).
- **Active development, large user base** — bug reports get fixed; the API has stabilized.

If NEX-48 (harrow's research) surfaces a better fit before code commits in rendering-heavy stories, we can revisit. Skeleton stories (CLI flag, WS intake, output routing) don't depend on the library choice.

## 6. Configuration

### 6.1 Invocation

`agora -keyfile <path> [-log-file <path>]`

- `-keyfile` supplies the aspect's identity + .bridle/.jira/.imap/.tty blocks.
- `-log-file` mandatory because stdout is the TUI. Defaults to `/tmp/agora-<aspect>.log` if unset.

### 6.2 Keyfile additions (optional)

```json
{
  "envelope": { ... },
  "encrypted_payload": "...",
  "jira": { ... },
  "imap": { ... },
  "tty": {
    "operator_name": "jacinta",   // appears as `From` on tty-sourced inbox items
    "history_depth": 1000,        // scrollback lines retained
    "input_history": 100          // REPL history length
  }
}
```

`.tty` fully optional; absence falls back to defaults (operator_name = "operator", depths = 1000/100). The .tty block being absent doesn't disable TTY mode — agora is always TTY. The block tunes the experience.

### 6.3 Env var compatibility

`NEXUS_UPSTREAM` and `NEXUS_OUTPOST` honoured identically to autonomous agentfunnel.

## 7. Frame catalogue (consumed and produced)

### 7.1 Frames consumed (inbound from nexus)

- `chat.deliver` — addressed to this aspect. Lands in FIFO inbox with `Source: "chat"`.
- `shutdown` (transport spec §13) — close TUI, deregister, exit cleanly within grace period.

### 7.2 Frames produced (outbound to nexus)

- `register` (on connect).
- `deregister` (on graceful exit).
- `chat.send` — when the engine calls `send_chat` tool OR when an inbox item's default-reply path resolves to "publish to bus."

### 7.3 Internal (shell ↔ TUI; no WS traffic)

- `notify_operator` (in-process tool) — engine writes a message to the operator's TUI without echoing to bus.

## 8. Routing rules (load-bearing v0 rule)

The aspect's funnel produces FinalText + optional tool calls per turn. Routing of FinalText is determined by two layers:

### 8.1 Source-tag default

The FIFO inbox item that triggered the turn carries a `Source` tag:

- `Source == "chat"` → default reply via existing `ChatGateway.SendChat`. Lands on the bus.
- `Source == "tty"` → default reply renders in the TUI chat panel only. No WS traffic.

### 8.2 Explicit tool overrides

The engine has explicit output tools available regardless of source:

- `send_chat` — forces a bus publish. Can be called multiple times per turn.
- `react_to` — toggle reaction on a message.
- `notify_operator` — pushes a private message to the operator's TUI, regardless of trigger source.

If the engine calls `send_chat` explicitly, the default-reply path is *additionally* whatever the source-tag rule says. To suppress the default for a chat-sourced turn, the engine returns empty FinalText or relies on the filter judge.

For tty-sourced turns: default reply goes to TUI. If the engine ALSO calls `send_chat`, that's the operator typing something private but the model deciding part of the response should be shared — useful and intentional.

### 8.3 notify_operator tool

```go
notify_operator(message: string, urgency?: "info" | "alert")
```

- `message`: free-form text. Rendered in TUI with distinct styling.
- `urgency`: optional. `info` (default) gets `⚡` prefix. `alert` gets `🚨` + warning color (v0.1).

Engine-side: registered in the funnel's tool kit only when agora's funnel is constructed (so autonomous aspects don't see it).

TUI-side: incoming notify-operator messages render in the chat panel with distinct styling. No bus traffic ever.

## 9. TUI layout

```
┌─[NEX] shadow @ nexus──── ws:connected · inbox:0 · turn:idle ────┐
│                                                                  │
│ [chat] keel → shadow:                                            │
│   build's yours.                                                 │
│                                                                  │
│ [chat] plumb → all:                                              │
│   acknowledged. closing the loop on my end.                      │
│                                                                  │
│ ⚡ shadow → you:                                                 │
│   keel and plumb both signed off on NEX-46. building it next.   │
│                                                                  │
│ you → shadow:                                                    │
│   what's the status on NEX-25?                                   │
│                                                                  │
│ shadow → you:                                                    │
│   cairn delay-policy spec at v0.3; 10 implementation             │
│   stories under NEX-25, blocked on three structural decisions   │
│   (NEX-33/34/35). Nothing else in flight there until the gates  │
│   clear.                                                          │
│                                                                  │
├──────────────────────────────────────────────────────────────────┤
│ > _                                                              │
└──────────────────────────────────────────────────────────────────┘
```

Three regions:

1. **Status line (top, single row)** — aspect name + nexus URL on the left; connection state, inbox depth, turn state on the right. Updated on every state transition.
2. **Chat panel (middle, scrollback)** — all message classes flow here, newest at bottom, auto-scrolls, pinnable.
3. **Input prompt (bottom, multi-line capable)** — operator types here. Multi-line via Shift-Enter; Enter submits; auto-expands.

### 9.1 Message classes (distinct styling)

| Source                       | Prefix                          | Color    |
| ---------------------------- | ------------------------------- | -------- |
| Incoming bus chat            | `[chat] <from> → <to>:`         | dim gray |
| Aspect's outbound bus chat   | `<aspect> → <to>:` (`→ chat`)   | normal   |
| Operator-only notify (info)  | `⚡ <aspect> → you:`            | accent   |
| Operator-only notify (alert) | `🚨 <aspect> → you:`            | warning  |
| Operator's typed message     | `you → <aspect>:`               | normal   |
| Aspect's tty-only reply      | `<aspect> → you:`               | normal   |
| System (errors, lifecycle)   | `[sys]`                         | dim      |

## 10. Streaming render

bridle's claudecode provider emits `ModelChunk` events as the model streams. agora plumbs these to the TUI via a bubbletea command.

Render approach:

- The bottom of the chat panel reserves a "live line" while the turn is in flight.
- Each `ModelChunk` appends to the live line and triggers a re-render of just that line (`viewport.SetContent` with the new buffer).
- Code blocks (` ```lang `) BUFFER until the closing fence; then re-rendered with syntax-aware styling. Avoids flicker on incomplete fenced blocks.
- On `TurnDone`, the live line is committed to scrollback at its final content; status-line `turn:` transitions to `idle`; cursor returns to the input prompt.

For v0: plain text render (no markdown). v0.1 adds glamour-based markdown render once we know the perf cost.

## 11. Backscroll and input behaviour

### 11.1 Backscroll

Default: pin-to-bottom. The chat panel auto-scrolls as new content lands.

If the operator scrolls up (PageUp, mouse wheel), the panel "unpins" and stays where they parked it. Status indicator (`▼ N new`) shows count of arrivals below since unpinning.

Re-pin: PageDown to bottom, Ctrl-End, or hitting Enter on an empty input prompt.

### 11.2 Multi-line input

- `Enter` submits (Slack-style).
- `Shift-Enter` inserts a newline.
- `Ctrl-J` also inserts a newline (terminal fallback for ttys where Shift-Enter is ambiguous).
- Pasting multi-line content auto-expands the input buffer; doesn't submit until Enter.
- `Ctrl-C` clears the current input. Two-Ctrl-C in succession (within 1s) triggers graceful exit.

### 11.3 Input while turn is in flight

Operator can keep typing. Submitted messages enqueue at the back of the FIFO inbox; the in-flight turn finishes first; the next inbox item triggers the next Deliberate. Queue-not-interrupt is the v0 rule.

## 12. Tool surface available to the engine

When agora runs, the funnel is constructed with this in-process tool set:

- `send_chat` (existing).
- `react_to` (existing).
- `chat.read` / `read_chat_message` / `read_chat_thread` (existing comms tools).
- `notify_operator(message, urgency?)` — NEW. Only registered in agora.

Plus all MCP-loaded tools via `.mcp.json` (nexus-jira, nexus-imap, etc.) — claude-code subprocess reads `.mcp.json` on each spawn.

## 13. Lifecycle and Outpost integration

### 13.1 Process lifecycle

1. Launch: `agora -keyfile shadow.keyfile.json`.
2. Read keyfile, validate, dial nexus (or local Outpost per `NEXUS_OUTPOST`).
3. Send `register`; await ack.
4. Open alternate-screen TTY; initialize bubbletea program with model.
5. Event loop: WS reads enqueue inbox items; bubbletea handles keys + renders; funnel-driven Deliberate fires on inbox not empty.
6. Graceful shutdown: operator hits `/quit` or `Ctrl-C × 2` → send `deregister` → close WS → restore terminal → exit 0.

### 13.2 Outpost-managed restart

Per transport spec v0.2 §13:

- Outpost can issue `aspect.restart` to swap the binary and respawn.
- agora receives `shutdown` within grace period → drains current turn → sends `deregister` → exits 0.
- Outpost respawns with new binary; new process opens its own TTY (operator sees brief restart, then panel returns with same identity).
- Mailbox-on-Outpost (§8.2) drains to the new process on register; chat from the restart window isn't lost.

### 13.3 Signal handling

- `SIGINT` (Ctrl-C from outside, or one Ctrl-C inside): begin graceful shutdown.
- `SIGTERM` (Outpost / OS): graceful shutdown, shorter grace.
- `SIGKILL`: no handler; OS reaps. Mailbox-on-Outpost catches missed traffic.
- `SIGWINCH` (resize): bubbletea handles natively; chat panel + input prompt reflow.

## 14. Inbox model

The funnel's FIFO inbox is reused unchanged (one turn pops one head item per #224). The change: items now carry a `Source` field.

```go
// Likely lives in bridle.InboxItem with an optional Source tag;
// agora supplies "tty" when enqueuing operator input. Chat-source
// items default to "chat" or empty (treated as chat).
type InboxItem struct {
    From       string
    Content    string
    MsgID      int64
    ReplyTo    int64
    ThreadRoot int64
    Source     string  // "chat" (default) or "tty"
}
```

Routing layer (§8) consults `Source` after the turn completes to decide channel.

## 15. Build sequence and dependencies

Story dependency graph (NEX-48..NEX-59):

```
NEX-48 (TUI library research)
  └─ Confirms / revisits §5 bubbletea choice; gates NEX-53, NEX-57

NEX-49 (TUI behaviour spec doc)            — satisfied by §9-11 of this document
NEX-50 (state boundary spec doc)           — satisfied by §4 of this document

NEX-51 (agora skeleton process)            — library-agnostic; starts now
NEX-52 (WS + chat.deliver intake)          — wire-through
NEX-54 (operator-input → inbox)            — needs NEX-51 + NEX-53
NEX-55 (output routing)                    — needs NEX-52 + NEX-54
NEX-56 (notify_operator tool)              — needs NEX-55

NEX-53 (TUI core)                          — needs NEX-48 confirm + NEX-49
NEX-57 (streaming render)                  — needs NEX-53

NEX-58 (build pipeline + Outpost deploy)   — needs NEX-51
NEX-59 (dogfood — switch shadow runtime)   — needs everything
```

Critical path: NEX-48 → NEX-49 → NEX-53 → NEX-57 → NEX-59. Off-path stories parallelize.

## 16. v0 acceptance criteria

- `agora -keyfile <kf>` opens a usable terminal panel.
- The aspect connects to nexus, sends `register`, receives `register.ack`.
- A `chat.deliver` frame addressed to the aspect appears in the chat panel within seconds of being sent on the bus.
- Operator typing a message + Enter:
  - Message appears in panel as `you → <aspect>:`.
  - Deliberation turn fires.
  - Model's reply renders as `<aspect> → you:`, streaming token-by-token.
  - No `chat.send` frame on the wire.
- A chat-triggered turn produces a `chat.send` frame on the bus.
- A turn that calls `notify_operator` produces a TUI-only message with distinct styling; no `chat.send` from the notify call.
- Terminal resize: panel + prompt redraw without corruption.
- Graceful exit via `/quit` or Ctrl-C × 2: sends `deregister`, restores operator shell cleanly.

## 17. Open questions for v0.1+

- Verbose vs curated mode toggle.
- Multi-aspect tabs.
- Slash commands handled in shell (each one needs a §4-compliant state-divergence story).
- Markdown rendering in chat panel via glamour.
- Persistent scrollback across restarts.

## 18. Build plan (the spec → code step)

Order:

1. NEX-50 + NEX-49: satisfied by this document (§4 + §9-11).
2. NEX-51: agora skeleton — main.go, alternate-screen open/restore, bubbletea init with empty model.
3. NEX-52: WS + chat.deliver intake via wsasp.Bridge → FIFO inbox.
4. NEX-53: TUI core — status line + chat panel + input prompt.
5. NEX-54: operator-input → inbox with Source: "tty".
6. NEX-55: output routing — Source-tag drives reply channel.
7. NEX-56: notify_operator tool registration + TUI render path.
8. NEX-57: streaming render — ModelChunk → live line.
9. NEX-58/59: deferred to operator-attended approval.

Each step gets its own commit so the progression is readable in the morning.

## 19. Status

v0.1 draft. Locks architecture, state boundary invariant, routing rules, TUI shape, behaviour rules, v0 build plan. Library choice committed provisionally. Approvals named in the header.
