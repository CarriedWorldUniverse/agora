# agora UI Redesign Design

**Date:** 2026-05-23
**Status:** Draft
**Scope:** Restructure agora's TUI for a "DM with shadow" feel. Address rendering, message-class confusion, in-place streaming (eliminate live-line double-paint), re-entry experience, dropped-submission feedback, and mouse-wheel capture.
**Non-goals:** New broker frames. Engine logic changes (funnel, bridle, wsasp untouched). New aspects/identity model. Markdown rendering, syntax highlighting, multi-aspect tabs. Bus-scrollback view (placeholder `/bus` command stub only). Root-cause fix of NEX-250/NEX-252 TTY double-fire (the dedupe workaround stays; we surface drops visibly).

## 1. Motivation

agora today is a chat-app shape applied to the wrong workload. The operator's actual use is:

- **shadow is the orchestrator.** The operator queues + specs work to shadow; shadow dispatches to peer aspects (keel, plumb, etc.) and reports back asynchronously.
- **agora = my interface to my orchestrator.** Not a chat room, not a bus monitor. The conversation is one-on-one with shadow.

The current UI fights this:

- **Bus traffic mixes with private chat** (even with the NEX-118 filter on, the operator sees `… N hidden` lines reminding them content is being suppressed).
- **Per-line `from: body` rendering with timestamps** makes every line look the same — operator-typed, shadow-replied, system, notify all blur together with subtly different colours.
- **Live-line streams below the chat region then re-paints to scrollback** — operator sees the same words twice (NEX-240's `StreamTextToChat` makes the gap worse).
- **Silent drops** (TTY dedupe, ≤1ms startup race) eat keystrokes with no feedback.
- **Mouse wheel scrolls the terminal's local buffer, not the conversation** — `tea.WithMouseCellMotion()` doesn't capture wheel events reliably across terminals.
- **Re-entry has no anchor** — operator walks away, returns, has to scroll up reading to figure out what changed.

This spec restructures the rendering and behaviour layers to fit the orchestrator-interface shape, without touching the engine or transport.

## 2. Goals and non-goals

**Goals:**
- Eliminate the live-line double-paint — stream tokens *into* the committed reply block.
- Replace per-line rendering with **speaker blocks** (header line + indented body) that make message classes visually distinct at a glance.
- Drop inline timestamps by default; surface session start in the status line; toggle inline timestamps with `Ctrl-G`.
- "Since you left" divider on re-entry (drops on next keystroke after ≥5 min operator idle).
- Visible system blocks for every drop / failure path (TTY dedupe drop, turn failure, broker error).
- Mouse wheel capture (`tea.WithMouseAllMotion()`); fallback hint in status line if wheel still doesn't fire within 30s.
- Improved keyboard scroll bindings (line scroll, scroll-while-typing).
- Improved history recall (prefix-match on textarea content, not "only when empty").
- Slash command inline discoverability (typing `/` shows completion hints).

**Non-goals:**
- Markdown rendering, syntax highlighting (separate v0.1+ polish).
- Multi-aspect tabs / multi-identity.
- Bus-traffic scrollback view (placeholder `/bus` stub only; full implementation later).
- New broker frames or transport changes.
- Engine logic changes — funnel, bridle, wsasp untouched.
- Root-cause fix of the NEX-250/NEX-252 TTY double-fire (the 15-min content-hash dedupe is kept as a workaround; this spec only surfaces drops visibly).
- Persistent scrollback across restarts.
- Crash-recovery state.

## 3. Layout

Three regions, top-to-bottom:

```
agora · shadow                 online · since 14:02 · ts:off    ← status (1 row, sticky top)
                                                                ← blank gap (1 row)
  you ────────────────────────────────────────────────────      ← chat region (fills H - 4)
    ship NEX-92 when keel's done

  shadow ─────────────────────────────────────────────────
    will do

  ─── since you left (2h 14m) ────────────────────────────

  ⚡ shadow ──────────────────────────────────────────────
    NEX-92 shipped (keel → main)
    NEX-87 needs your eyes

──────────────────────────────────────────────────────────      ← divider (1 row)
› _                                                             ← input (1-6 rows, sticky bottom)
```

**Status line** (single row): `agora · <aspect>` left-aligned; `<connectivity> · since <HH:MM> · ts:<on|off>` right-aligned. Three pieces only. `since 14:02` is the session start; `ts:off` indicates timestamp toggle state (default off).

**Chat region** (`height - 4`): scrollable via bubbles/viewport. Renders speaker blocks (Section 4). No bounding box, no top divider — the status line and input divider already frame it.

**Input divider** (single row): one horizontal rule above the textarea. The current top-of-chat-region divider is dropped.

**Input** (1-6 rows): unchanged behaviour, auto-grows with content (textarea, `maxInputLines=6`).

Net: one fewer chrome row than today; no "boxed in" feel; status info still pinned during scroll.

## 4. Speaker blocks and message classes

Replaces today's per-line rendering. Each "turn worth of output" is one **block**.

### 4.1 Block classes

```
  you ────────────────────────────────────────                ← amber, bold header
    operator-typed message body                               ← normal body

  shadow ─────────────────────────────────────                ← green, bold header
    aspect's reply (panel-route OR mirror of outbound chat)   ← normal body

  shadow · thinking ──────────────────────────                ← green, italic header
    streaming tokens land here, in place▌                     ← normal body, cursor at tail

  ⚡ shadow ──────────────────────────────────                ← pink, bold header
    notify-operator content                                   ← pink, dim body

  · system ───────────────────────────────────                ← grey, italic header
    duplicate of "ship NEX-92..." submitted 4m ago — wait     ← grey body

  ─── since you left (2h 14m) ─────────────────               ← divider class, dim
```

- **Header line** carries the entire identity: speaker, optional state suffix (`· thinking`, `· failed`), trailing rule line that fills to width.
- **Body lines indented 2 spaces.** Wrapping respects the indent (lipgloss width = `viewport.Width - 2`).
- **No `from:` prefix on every body line.** A multi-paragraph reply has one header, then N body paragraphs.
- **Consecutive blocks from the same speaker coalesce at render time** — two `blockAspect` blocks in a row render as one block with a blank line between bodies. Storage stays raw (one entry per turn).
- **Block class drives both header colour and body colour.** No "everything is bold + colour, just slightly different."
- **Timestamps off by default.** With `Ctrl-G` (or `/ts`) they appear inline at the right edge of the header line: `you ──────── 14:32`.

### 4.2 Block class enum

```go
type blockClass int

const (
    blockYou             blockClass = iota // operator-typed input echo
    blockAspect                            // aspect's reply (panel OR chat mirror)
    blockAspectThinking                    // active streaming, ends at TurnDone
    blockNotify                            // notify-operator content
    blockSystem                            // dropped submission, error, banner
    blockDivider                           // since-you-left, session start
)
```

### 4.3 chatBlock struct

```go
type chatBlock struct {
    class     blockClass
    speaker   string         // "you", "shadow", "system" — drives header text
    body      strings.Builder // mutated in place during streaming
    createdAt time.Time      // for inline timestamp render
    failed    bool           // toggled on TurnFailed to swap header styling
}
```

### 4.4 Rendering

`renderChatBlock(b chatBlock, width int, showTS bool) string` produces:

1. Header line: `<glyph?> <speaker> <state?> <rule chars to fill width> [<HH:MM if showTS>]`
2. Body: `body.String()` wrapped to `width - 2`, every line prefixed with two spaces.

`coalesceBlocks([]chatBlock) []chatBlock` folds consecutive same-`speaker` + same-`class` blocks into one (joining bodies with a blank line). Called at render time; storage stays one entry per logical event.

### 4.5 Bus traffic does not paint

`chat.deliver` frames continue to flow through `bus.onDeliver` → engine inbox (the aspect's turn fires off them). They are **not** rendered in the UI scrollback. Concretely:

- `cmd/agora/main.go`'s `onChat` callback drops the `p.Send(ui.ChatDelivered{...})` call entirely. Only `eng.Receive(...)` remains.
- `ui.ChatDelivered` is removed from the Model's message set.
- `markOperatorRelevant` and `filterChatter` are removed wholesale — there is no UI-side filter because there is no UI-side bus rendering.
- @-mentions are **not** special-cased in the UI. If a peer mentions the operator, shadow sees it in its turn context and is expected to use `notify_operator` to surface it. UI has no knowledge of operator identity beyond the configured `OperatorName` for the `you` block label.

This is the operational consequence of "aspect surfaces what matters" — the substrate carries everything, but the operator only sees what shadow elevates.

## 5. In-place streaming

Today's flow (which produces the double-paint):

1. Operator submits → funnel `RunTurn` → bridle `ModelChunk` events.
2. `UIHook` → `ui.ModelChunk{Text}` → Model accumulates into `streamBuffer` → renders to `liveLine` *below* the chat region.
3. On `TurnDone`, Model clears `liveLine` + `streamBuffer`.
4. `AgoraReturnHandler.Handle` separately fires `ChatPanelReply` or `ChatSent` → adds a *new* chatLine with the same content.

The fix:

- `AgoraReturnHandler.OnTurnStart` fires `ui.TurnStarted{Source, MsgID}` (today: no-op).
- Model appends a new `blockAspectThinking` block immediately with empty body; records `activeBlockIdx = len(blocks) - 1`.
- Each `ui.TurnChunk{Text}` (renamed from `ModelChunk`) appends to `blocks[activeBlockIdx].body`; viewport re-renders.
- On `ui.TurnDone{}` (renamed from `ModelTurnEnd`): mutate `blocks[activeBlockIdx].class = blockAspect`; clear `activeBlockIdx = -1`.
- `AgoraReturnHandler.Handle` **no longer fires `ChatPanelReply` or `ChatSent` for FinalText.** The wire emission for `SourceChat` is unchanged (still calls `bus.SendChat`). UI side: nothing to render — the block is already there.
- On bus send failure (`SourceChat`): fire `ui.TurnFailed{Reason}` → Model sets `blocks[activeBlockIdx].failed = true`; header re-renders as `· failed: <reason> ────` in error styling. Body content (whatever streamed before the failure) stays visible. A system block follows: `· system · /retry to re-run this turn`.

Code-fence buffering (`renderStreamingLine` masking partial fences) stays, called inside the block's body render when `class == blockAspectThinking`.

## 6. Re-entry experience

Bookkeeping fields on Model:

- `lastInteractionAt time.Time` — updated on every `tea.KeyMsg`.
- `idleSince time.Time` — set when idle threshold first crosses.
- `awaitingReentry bool` — true between idle threshold cross and next keystroke.

Behaviour:

- New internal message `idleTick` fires every 60s via `tea.Tick`.
- On each `idleTick`: if `time.Since(lastInteractionAt) >= 5*time.Minute` AND `!awaitingReentry`, set `idleSince = lastInteractionAt` and `awaitingReentry = true`. Nothing visible yet.
- Track `blocksDuringIdle int` — incremented by any non-divider block appended while `awaitingReentry == true`.
- On next keystroke after `awaitingReentry == true`:
  - If `blocksDuringIdle > 0`: append a `blockDivider` with body `since you left (<duration formatted as Xh Ym>)`.
  - Else: do nothing visible (idle had no content; a divider would be noise).
  - Either way: clear `awaitingReentry`; reset `idleSince`; zero `blocksDuringIdle`.

Why "next keystroke" rather than "now": dropping a divider unprompted just adds noise next time the operator looks. Re-entry = keystroke; that's the right anchoring moment.

Why gated on `blocksDuringIdle > 0`: an operator who paused for a phone call and returned to a quiet panel shouldn't see a divider for nothing.

The divider is a `blockDivider` (not chat content) — doesn't trigger auto-scroll if the operator is scrolled up, doesn't get duplicate-rendered on resize.

**Scope of "what changed":** purely visual. The divider marks the boundary; actual notify-operator + chat content that arrived during idle is rendered normally (as blocks) above the divider. We are not summarising on the fly — the aspect's notify-operator content is the summary. Good shadow hygiene = good summary; this is a shadow-prompt-tuning concern, not a UI concern.

## 7. Submission feedback

Three drop paths today; each gets visible feedback.

### 7.1 TTY dedupe drop

`engine.Receive` today drops a TTY duplicate silently. Add `Config.OnDrop func(reason string, firstSeen time.Time)`; call it from the dedupe path. `main.go` wires this to a callback that calls `p.Send(ui.SubmissionDropped{Reason, FirstSeen})`. Model renders as a `blockSystem`:

```
  · system ──────────────────────────────────────
    dropped duplicate — same line submitted 4m ago.
    Modify the message or wait 11m more to resend.
```

Engine has no bubbletea dependency — the callback boundary keeps the layering clean.

### 7.2 Startup race (≤1ms onSubmit-nil window)

Textarea is **disabled** at Model construction with placeholder `agora starting…`. On `ui.RegisterSubmit`, Model enables it and replaces placeholder with the normal prompt (`type to <aspect>; shift+enter for newline; /exit to quit`).

Operator typing before registration just sees a non-responsive textarea — clear feedback, no silent drop.

### 7.3 Turn failure

- For a turn whose block is already on screen (Section 5): mutate `blocks[activeBlockIdx].failed = true` and class to `blockAspect`; header renders as `· failed: <short reason> ────` in error styling. Body stays.
- Follow with a `blockSystem`: `/retry to re-run this turn`.
- Add `/retry` command — re-pushes the last triggering inbox item via the same engine path.

## 8. Mouse and keyboard input

### 8.1 Mouse wheel

Switch `tea.WithMouseCellMotion()` → `tea.WithMouseAllMotion()` in `cmd/agora/main.go`. This unlocks wheel events in most terminals.

Fallback hint: status line shows `wheel:off` (instead of just the connectivity state) if no `tea.MouseMsg` of wheel type has fired within 30s of startup. Updates to `wheel:on` once one is observed.

### 8.2 Keyboard scroll

Consolidated bindings:

| Key | Action | Notes |
|---|---|---|
| `Page Up` / `Page Down` | one page scroll | kept |
| `Ctrl-U` / `Ctrl-D` | half page | kept |
| `Ctrl-E` / `End` | jump to bottom | kept; clears `unreadBelow` |
| `Ctrl-A` / `Home` | jump to top | kept |
| `Ctrl-K` / `Ctrl-J` | one line up / down | **new**, only when textarea empty |
| `Alt-Up` / `Alt-Down` | one line up / down | **new**, always (scroll while typing) |
| `Ctrl-G` | toggle inline timestamps | **new** |
| `Ctrl-Enter` | submit (alias for Enter) | **new**, for terminals where Shift-Enter is ambiguous |

### 8.3 History recall

Simplified — typing prefix + `Up` recalls last matching entry.

- `Up` / `Down` first checks: does textarea contain text matching any history entry's prefix (starts-with on first line)?
- If yes: cycle through matching entries.
- If no AND textarea is empty: full history browse (current behaviour).
- If no AND textarea has content: cursor positioning (current behaviour at non-edge lines).

Operator no longer needs to remember "history only when empty."

### 8.4 Submission keys

- `Enter` submits (unchanged).
- `Shift-Enter` / `Alt-Enter` inserts newline (unchanged).
- `Ctrl-Enter` also submits (new alias).

### 8.5 Quit / interrupt

Unchanged from today:

- `Ctrl-C` once → graceful (deregister, drain, exit).
- `Ctrl-C` twice within 1s → hard quit.
- `/exit` slash command.

### 8.6 Slash command discoverability

Typing `/` in an otherwise-empty textarea opens an inline hint below the input divider:

```
commands: /exit /help /retry /ts /bus
```

Rendered with system styling. Disappears as soon as the textarea contains more than `/`.

`Tab` completes the longest unique prefix. (Bubbles textarea doesn't have native tab-complete; we intercept `tea.KeyMsg{Type: tea.KeyTab}` when the textarea contents start with `/`.)

## 9. Code structure

No new packages. File-level changes within `internal/{bus,engine,ui}`:

### 9.1 `internal/ui/chat.go` — rewrite rendering layer

- Add `blockClass` enum (Section 4.2).
- Add `chatBlock` struct (Section 4.3).
- Replace `chatLine` with `chatBlock` as scrollback primitive.
- Replace `appendChatLine` with `appendChatBlock` + `appendToActiveBlock` (mutates trailing block's body in place).
- New: `renderChatBlock(b chatBlock, width int, showTS bool) string`.
- New: `coalesceBlocks([]chatBlock) []chatBlock`.
- Drop: `chatClass`, `renderChatLine`, `stylePrefixBody`, `markOperatorRelevant`.
- `renderStreamingLine`'s code-fence buffering survives — called from inside the active block's body render when `class == blockAspectThinking`.

### 9.2 `internal/ui/` Model split

Today's `model.go` is 666 lines and mixes struct/lifecycle, keystroke handling, scroll behaviour, block management, and message-type declarations. The rewrite splits it into four files at responsibility boundaries:

#### 9.2.1 `internal/ui/model.go` — core Model + bubbletea lifecycle (~150 lines)

- `Model` struct (all fields).
- `Config` struct.
- `NewModel(cfg) Model` constructor.
- `Init() tea.Cmd` — starts `textarea.Blink`, `wsTick`, `idleTick`.
- `Update(msg) (Model, Cmd)` — top-level dispatcher only; delegates per-message to handlers in sibling files.
- `View() string` — renders status + chat region + divider + input.
- `renderStatus()`, `chatHeight()`, helper `max(int,int)`.

#### 9.2.2 `internal/ui/messages.go` — tea.Msg type declarations (~50 lines)

All `tea.Msg` types currently scattered through `model.go`:

- Surviving: `InboxUpdated`, `RegisterSubmit`, `NotifyOperator`, `ReadyToQuit`, `wsTick`.
- Renamed: `ModelChunk` → `TurnChunk`, `ModelTurnEnd` → `TurnDone`.
- New: `SubmissionDropped{Reason, FirstSeen}`, `TurnStarted{Source, MsgID}`, `TurnFailed{Reason}`, `idleTick`.
- Dropped: `ChatPanelReply`, `ChatSent`, `EngineError`, `ChatDelivered`.

Pure type declarations, no behaviour. Keeps the message contract scannable in one file.

#### 9.2.3 `internal/ui/input.go` — keystroke handling (~180 lines)

- `handleKey(msg tea.KeyMsg) (Model, Cmd)` — the big switch from today's `Update`.
- `historyBack()`, `historyForward()`, new `historyPrefixMatch(prefix string)`.
- `resizeInputForContent()`.
- Slash-command dispatch path (calls into `commands.go`).
- Tab-completion handler.
- Updates `lastInteractionAt` and drops re-entry divider when `awaitingReentry`.

#### 9.2.4 `internal/ui/blocks.go` — block lifecycle (~140 lines)

- Block-level mutators: `appendBlock(b chatBlock)`, `appendToActiveBlock(text string)`, `markActiveBlockFailed(reason string)`, `finishActiveBlock()`.
- Tea-msg handlers that mutate blocks: `handleTurnStarted`, `handleTurnChunk`, `handleTurnDone`, `handleTurnFailed`, `handleNotifyOperator`, `handleSubmissionDropped`, `handleIdleTick`.
- `refreshChatContent(forceBottom bool)` — calls `renderChatContent` from `chat.go`.
- Idle-tracking helpers: `markInteraction()`, `checkIdle()`.

This split keeps each file under ~200 lines, each with one clear responsibility. The Model struct itself stays in `model.go` so all sibling files can access fields via method receivers.

**Fields summary (lives on `Model` in `model.go`):**

- Replace `chat []chatLine` → `blocks []chatBlock`.
- Replace `streamBuffer` / `liveLine` → `activeBlockIdx int` (-1 when idle).
- Add `lastInteractionAt`, `idleSince`, `awaitingReentry bool`, `blocksDuringIdle int`.
- Add `showTimestamps bool` (toggled by Ctrl-G).
- Add `wheelObserved bool` (for `wheel:off` hint).
- Add `textareaEnabled bool` (for startup-race fix).
- Drop `filterChatter` field + all references; keep `unreadBelow`.

### 9.3 `internal/ui/commands.go` — small additions

- Add `/retry`, `/ts`, `/bus` (placeholder; prints `not yet implemented`).
- `dispatchCommand` shape unchanged.
- New: tab-completion path (called from Model's `tea.KeyTab` handler, not from `dispatchCommand`).

### 9.4 `internal/ui/styles.go` — new file

- Move all `lipgloss.Style` declarations out of `chat.go` into a dedicated file.
- ~30 lines.

### 9.5 `internal/engine/engine.go` — callback addition

- Add `Config.OnDrop func(reason string, firstSeen time.Time)`.
- Call from `Receive` when dedupe drops a TTY submission.
- Existing log line stays for observability; the callback is the operator-feedback path.

### 9.6 `internal/engine/agora_return_handler.go` — turn lifecycle messages

- `OnTurnStart` fires `ui.TurnStarted{Source, MsgID}` (today: no-op log only).
- `Handle` no longer fires `ChatPanelReply` or `ChatSent` for FinalText. Wire emission for `SourceChat` unchanged.
- On bus send error: fire `ui.TurnFailed{Reason}` instead of `ui.EngineError`.
- `extractNotifyBlocks` still runs; each notify still fires `ui.NotifyOperator{Body}` → Model renders as `blockNotify` block.

### 9.7 `internal/engine/ui_hook.go` — rename messages

- `OnBridleEvent`: `bridle.ModelChunk` → `ui.TurnChunk{Text}` (was `ui.ModelChunk`); `bridle.TurnDone` → `ui.TurnDone{}` (was `ui.ModelTurnEnd`).
- `BeginTurn` no-op (`TurnStarted` comes from the return handler, the authoritative trigger metadata source).

### 9.8 `cmd/agora/main.go` — wiring

- Pass `engine.Config.OnDrop` callback that calls `p.Send(ui.SubmissionDropped{...})`.
- Swap `tea.WithMouseCellMotion()` → `tea.WithMouseAllMotion()`.
- `onChat` callback: drop the `p.Send(ui.ChatDelivered{...})` call (per §4.5). Only `eng.Receive(...)` remains.

### 9.9 Tests

- `internal/ui/chat_test.go` (new): block rendering, coalesce, streaming append, divider rendering, timestamp toggle render.
- `internal/ui/model_test.go` (new): idleTick + re-entry divider drop, dropped-submission rendering, disabled-textarea-at-startup, history prefix recall, Ctrl-G toggle.
- `internal/engine/engine_test.go`: extend with OnDrop callback assertion.
- `internal/engine/agora_return_handler_test.go` (new): Handle no longer emits ChatPanelReply/ChatSent for FinalText; OnTurnStart emits TurnStarted; bus send error emits TurnFailed.

### 9.10 File sizes after rewrite (estimated)

| File | Before | After |
|---|---|---|
| `chat.go` | 202 | ~180 |
| `model.go` | 666 | ~150 (core + lifecycle only) |
| `messages.go` | — | ~50 (new — tea.Msg types) |
| `input.go` | — | ~180 (new — keystroke + history) |
| `blocks.go` | — | ~140 (new — block lifecycle) |
| `styles.go` | — | ~35 (new — lipgloss styles) |
| `commands.go` | 124 | ~140 |
| `engine.go` | 205 | ~220 |
| `agora_return_handler.go` | 143 | ~120 |

`model.go` shrinks from 666 to ~150 by extracting input, blocks, messages, and styles into siblings. Each new file has a single responsibility and stays under 200 lines.

Nothing crosses a structural boundary; the Model/View/Update pattern is unchanged; bridle/funnel/bus integration untouched.

## 10. Migration / rollout

Single PR. No persistent state changes (no on-disk format change, no protocol change). Operator restarts agora once; new behaviour active. The `/bus` placeholder (Section 8.6) prints `not yet implemented`, leaving room for the bus-scrollback view as a follow-up.

**One user-visible behaviour break:** bus `chat.deliver` frames stop painting in the UI. Operators who were relying on `Ctrl-T` to see all cluster chatter will need to wait for the `/bus` follow-up. Today's NEX-118 default (filtered) means most operators were already not seeing this content; the change closes the escape hatch. Worth a note in the PR description.

## 11. Risk

- **Wheel capture still fails on some terminals.** Mitigation: fallback hint in status line, robust keyboard scroll bindings, documented in README.
- **Block coalescing surprises.** Two distinct `blockNotify` arrivals fold into one visual block — operator might not realise two events occurred. Mitigation: render the coalesced block with a blank line between bodies, plus an empty `· ────` separator if `createdAt` deltas exceed 60s.
- **History prefix-match may surprise on common short prefixes.** Operator types `s`, hits Up, gets a previous `ship NEX-92...`; expected `status?`. Mitigation: prefix-match falls back to full-history browse if more than one entry matches; show match count inline in the input placeholder.
- **Disabled textarea at startup feels broken** if the bus is slow to validate the keyfile (offline, broker unreachable). Mitigation: placeholder is `agora starting…`, not blank; status line shows `offline` immediately so the operator has a signal.
- **Idle threshold (5min) too aggressive.** Addressed in §6 by gating the divider on `blocksDuringIdle > 0` — silent idle windows produce no divider.

## 12. Open questions for follow-up

- Should `/bus` open an inline scrollback (separate viewport region toggled in) or a pop-out view (alt-screen swap)? Out of scope here; flagged for design later.
- Should `notify_operator` blocks support a "dismiss" action so they fold/collapse once read? Useful when the operator triages a batch.
- Should the status line surface in-flight count (work shadow has dispatched to peers but not yet heard back)? Requires shadow to emit a structured "I dispatched X" notify; punted.
- Should consecutive same-day session-start dividers be suppressed? Cosmetic.

## 13. v1 acceptance criteria

- agora opens with the new layout (status line + chat region + input divider + textarea).
- Operator types a message + Enter: appears as a `you` block; shadow's reply streams in-place as a `shadow · thinking` block; on TurnDone the block class becomes `shadow` (no double-paint).
- A `notify_operator` fenced block produces a `⚡ shadow` block; no bus traffic painted alongside.
- No `chat.deliver` frame paints in the UI scrollback — including @-mentions (changed from today; `markOperatorRelevant` / `filterChatter` removed wholesale). chat.deliver still flows to the engine inbox; shadow is responsible for surfacing what the operator should see via `notify_operator`.
- A TTY duplicate within 15 min produces a visible `· system` block explaining the drop.
- Operator idle ≥5 min, then keystroke: `─── since you left (Xh Ym) ────` divider appears at the right position. Repeated idle/return cycles drop one divider per cycle.
- `Ctrl-G` toggles inline timestamps; status line `ts:` field reflects state.
- `Ctrl-K` / `Ctrl-J` (when input empty) scroll one line; `Alt-Up` / `Alt-Down` scroll one line regardless of input state.
- Mouse wheel scrolls the agora chat region (assuming `tea.WithMouseAllMotion()` capture works on the operator's terminal); `wheel:off` hint appears if not.
- Typing `/` in empty textarea shows the command hint line; `Tab` completes.
- `/retry` re-runs the last triggering turn after a `TurnFailed`.
- Graceful exit (`/exit`, Ctrl-C×2) works as today.
