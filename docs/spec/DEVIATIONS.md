# Spec ↔ code deviations (U0–U18 build)

The `agora-spec-*.md` files are the **design of record**. The U0–U18 build tracked
them closely, but in a handful of places the shipped code deliberately differs
from, defers, or gets stricter than the spec text. This file is the ledger of
those gaps so "what we designed" vs "what we built" is written down rather than
living only in PR bodies. Keep it current as the specs and code converge.

Status as of main `7ef1b47` (2026-07-17), agora epic NEX-743, units U0–U18 merged.

## 1. The turn engine is BUILT (Phase 2) — this entry is now historical
**Revised 2026-07-20** — the original entry here ("the real turn engine is
unbuilt", the largest deliberate gap) was true at U18 but is stale: Phase 2
(NEX-777 → NEX-789, plus live-turn fixes #68–#71) shipped
`internal/turnengine`, a real `io.Engine` over the bridle Harness (funnel
mode): the approval-gated tool loop (NEX-781 `BeforeToolCall`; NEX-782
`KindRead`), `ProfileConfig` model/system-prompt/policy resolution
(NEX-783), protocol-complete tool-call item events (NEX-784, §11), thread
durability + per-thread claude-sdk session resume (NEX-785), the ctxmap
context engine (NEX-787), and the in-process launch path (NEX-789 — bare
`agora` runs the real claudesdk lane with no daemon). Live operation is the
daily driver. The conformance suite (U18) still drives **scripted** engines
(`io.ScriptedEngine`, `conformance/flow_engine.go`) by design — fixtures
don't burn subscription tokens.

**Revised 2026-07-27 (agora#140)** — the two items this entry listed as
"still open" have BOTH since landed. They are recorded here as closed
rather than deleted, because this file's job is the design-vs-code ledger
and a reader who remembers the gap needs to find out it is gone:

- **plan/question special-casing — DONE.** `contracts.ToolQuestion` and
  `contracts.ToolPlan` are real tools on the surface
  (`toolrunner/planning.go`), wired via `toolrunner.NewPlanningFamily()`
  (turnengine/manager.go). `question_response` is no longer a no-op: a
  blocking `question` registers a waiter keyed by the question id the
  `QuestionLog` mints, and `InQuestionResponse` resolves it
  (turnengine/planning.go, `resolveQuestionWaiter`; Run's input loop in
  manager.go). Park-then-resume is pinned by
  `conformance/flow_question_park_resume_test.go`.
- **MCP wiring — DONE.** `internal/mcp` (U8) is consumed: `buildMCPSource`
  (cmd/agora/mcpsource.go) is passed to `turnengine.WithMCPSource` at the
  shared engine seam (cmd/agora/engine.go), stored on the Manager, and
  handed to `toolrunner.NewSurface` (turnengine/manager.go) — so all three
  lanes (TUI, `agora pipe`, `agora daemon`) get it. Project-scoped servers
  are fail-closed behind a user-layer content-hash trust gate
  (cmd/agora/mcptrust.go). Covered by `cmd/agora/mcpsource_test.go`
  (gating, startup-failure surfacing, `Close()` on run end).

  Note the mechanism is still as described: claudesdk reports
  `SupportsMCP=false`, so MCP tools ride in `Tools` rather than
  `TurnRequest.MCP`, and `mcp__`-prefixed calls classify into
  `ItemMCPToolCall` wire events (sink.go). "Unconsumed" was the stale part,
  not the transport note.

**Ledger hygiene:** this entry sat stale for a week and, during an
evaluation pass, two independent readers took "MCP is unconsumed" as
ground truth and nearly reported a live gap that did not exist. A stale
entry here is worse than no entry, because this is the file people consult
specifically to learn what is missing. When a gap closes, revise it in the
same change that closes it.


### fs tool names match Claude's native surface (2026-07-26)

The fs family advertises `Read`/`Write`/`Edit`/`Glob`/`Grep` with a
`file_path` argument, rather than the snake_case `read_file`/`write_file`/
`edit_file` with `path` the spec text describes. A model reaches for the
tool it was trained on: with the old names, sessions repeatedly emitted a
native-shaped call first, took an unknown-tool or bad-args error, and
retried. Matching the native surface removes that retry class.

`ListDir` has no native counterpart (Claude shells out), so that name is
ours; it is PascalCase only for consistency.

The legacy names and the `path` argument are still ACCEPTED — including
mixed pairings such as `Write` + `path` — so threads persisted before the
rename replay unchanged. They are simply no longer advertised.

**External contract note:** hook payloads (`PreToolUse`/`PostToolUse`
`tool_name`) now carry the advertised name, so a user hook matcher keyed on
`write_file` must be updated to `Write`.

### `warning` wire event for non-terminal notes (2026-07-27)

Adds `EvWarning` (`"warning"`) with a `{message, stage}` payload, carrying
bridle's benign `TurnError` stages (`retry`, `provider_api_error`,
`resume_fallback`) and `bridle.Warning`.

Previously these were DROPPED, to stop a successful turn rendering agora's
terminal red `error:`. Silence was wrong for the resume fallback: the turn
quietly restarts on a fresh provider session and the prior provider-side
context is gone, with nothing in the stream saying so (agora#120). The
choice was never error-vs-silence; it needed a third severity.

`stage` carries bridle's `TurnErrorStage` verbatim so a consumer can filter
by cause rather than pattern-matching prose. The TUI renders it as
`note: …` in the idle status row, strictly BELOW `error:` in precedence,
and clears it on the next `turn.started` so a note cannot misattribute
itself to a later turn.

## 2. Conformance (U18): structural, not byte-exact, comparison for two flows
`TestFlowQuestionParkResume` and `TestFlowPodProvision` assert **structural
equivalence** (event-type sequence + id self-consistency + byte-exact on all other
fields incl. `TurnID`/`Item`/`By`) instead of byte-matching the golden fixture,
because the real seams (`planning.QuestionLog.Ask`, `pod`) mint IDs via
`crypto/rand` with no injectable determinism — byte-matching the fixture's literal
`qu_0001`/etc. would mean *not* driving the real seam. If a deterministic-ID seam
is added to `planning`/`pod` later, these can become byte-exact. The other 4 flows
(turn, approval, plan-gate, compaction/curation) are byte-exact.

## 3. Daemon auth defaults fail-closed (U18) — stricter than the spec implied

**Revised 2026-07-27 (agora#133).** Fail-closed was right; having nothing on
the other side was not. `cmd/agora`'s `runDaemon` never configures a
`Registry`, so EVERY client fell to `CapObserver` and `Session.handleInput`
refused every input needing `CapInteractive`/`CapApprover` — the shipped
`agora daemon` could not serve a turn to anyone, including the operator who
started it. It was silent too: `dialBackend` PREFERS a listening daemon, so
starting one turned the TUI into a window that rendered normally and dropped
everything typed into it.

`authenticate` now takes the TRANSPORT into account. A connection arriving
on the unix socket is granted `CapObserver+CapInteractive+CapApprover`,
because `io.ListenUnix` chmods that socket to 0700 at creation — the kernel
refuses any other uid's `connect()`, so the peer is necessarily the operator
who started the daemon. That is the same trust boundary the in-process lane
already runs at. `CapAdmin` is deliberately NOT granted: admin operations
stay behind a real registry identity, since "is the local uid" is a weaker
claim than it looks once anything else runs as that user. The ws lane is
unchanged and still fails closed to `CapObserver`.

Client side, the TUI now warns when its own `client.attached` reports
capabilities that cannot send input, so a read-only attach on any lane says
so instead of silently swallowing messages.

- A **nil-`Registry` daemon grants `CapObserver` only** by default; it does NOT
  trust wire-declared `AttachRequest.Capabilities`. `Config.InsecureTrustWireCaps`
  is an explicit dev/test opt-in (loud stderr warning at `NewDaemon`). This closed
  a fail-open the review gate found (a client could otherwise self-declare
  `CapAdmin`/`CapApprover` and bypass the whole approval gate).
- `remote.CheckApproval` (per-device `AllowedApprovalKinds` scoping) is enforced on
  the **live** approval-resolution path, not just the offline queue.

## 4. Daemon pod-mode is control-level only (U18)
`Daemon.PodMode(identities, engineFactory)` drives `pod.Pod.Provision`/`RunTurn`
sharing the daemon's `store`/`clock`/`questions`, but does **not** stream pod
events to multi-attached clients — `internal/pod` is frozen and exposes no
observer/second-attach hook, which would need a pod API change. There is **no
`agora daemon --pod` CLI flag** yet: `cmd/agora`'s daemon has no real
`EngineFactory` to plug in until the turn engine exists.

## 5. Approval-request payload wire shapes were defined by U15, not the spec
`contracts.ApprovalRequest.Payload` is typed `any`. U15's TUI defined the
kind-specific shapes it renders in the approval modal, and the real daemon /
turn-engine **must emit these exact shapes** or the modal renders blind:
- `exec` / `gate` → `{ "command": "..." }`
- `patch` → `{ "path": "...", "lines": [ { "kind", "oldNo", "newNo", "text" } ] }`
- `mcp_tool` → `{ "tool": "...", "args": ... }`
- `escalation` → `{ "detail": "..." }`
- `read` (NEX-782, post-spec addition — `contracts.KindRead`, read-only fs
  tools) → `{ "detail": "..." }`: the path for Read/ListDir, the
  pattern for glob/grep. This modal only ever renders under the `strict`
  preset — every other built-in preset auto-allows `read`.
(U18's conformance approval flow emits the `exec` shape accordingly.)

## 6. Workflows (U14): v1 scope limits (mostly per spec §7)
- `ctx.budget` is **stubbed** — no token enforcement; the starlark step budget
  (`SetMaxExecutionSteps`, default 1e9) + a lifetime branch-goroutine cap are the
  DoS backstops.
- Nested `ctx.workflow()` returns an honest unimplemented error.
- Model/effort **alias resolution is deferred to bridle's registry** at run time
  (pass-through in-engine, not validated).
- `args_schema` is parsed but not validated.
- A **bubbled agent-question** ships the v1-minimum ("the human answer becomes
  `ctx.agent()`'s result", journaled) rather than a full `subagent.Manager`
  Continue-based re-invocation.
- The run's frozen `now` is **persisted in the journal** (`EntryRunStart`) so
  `ctx.now` is stable across resumes.

## 7. Context (U12 / U18): wire builders added; Compact is a no-op
- `ctxmgr` gained `NewCompactionStartedEvent` / `NewCompactionCompletedEvent` (U18)
  — the spec named the `thread.compaction.started`/`.completed` wire shapes but the
  builders didn't exist until the conformance flow needed them.
- **Revised 2026-07-27 (agora#134).** `Manager.Compact` was a documented no-op
  (curation runs continuously inside `Assemble`, per context-spec §1). That reading
  was locally correct and globally wrong: its ONLY production caller is the
  context_length compact-and-retry, which needs the request to actually shrink — and
  the working-set budget covers keyed tool artifacts only, so dialogue text had no
  eviction path at all and `DialogueKeepTurns` sat in `DefaultConfig` unread.
  `Compact` now **arms a sticky dialogue trim** that `Assemble` honours (keep the
  last `DialogueKeepTurns` user messages, stub the dialogue before them). It is
  still not a compaction episode in the spec's sense — it changes what the NEXT
  assembly renders rather than curating in place — so `TokensAfter` is a measured
  figure only once the caller reassembles. `runCompactionEpisode` therefore takes a
  `reassemble` callback and reports the real number after it runs; the manual
  `/compact` path passes nil (it runs between turns, with no request to rebuild)
  and honestly reports no measured reduction, the trim landing on the next turn.
  §5's real dialogue *summarization*, as opposed to stubbing, remains future work.

## 8. Prompt (U4 / U15): Compile deferred
`prompt` verbs `Compile()` returns `ErrNotImplemented` — an **interactive
exception** (`agora prompt compile` has its own eval gate, build-spec §0 ground
rule 5); the TUI consumes an already-`Resolve`d core, not `Compile`. `verbs.New()`
gained `destDir` path-traversal containment (`ContainDestDir`, incl. cross-platform
absolute-name rejection).

## 9. Skills / AGENTS (U5 / NEX-750): symlink containment tightened
The spec (`agora-spec-skills.md` §2) was **refined in-build** and now reflects the
shipped behaviour: Repo-scope roots follow symlinks but **contained within the
project root** (an untrusted clone can't symlink out to read arbitrary host files);
User/Admin roots roam; System never follows. (The retired home-dir spec copy still
carried the pre-fix wording — one reason the duplicates were retired.)

### run_command escalates egress to INTERNAL addresses (2026-07-27, agora#136)

`commandNamesOutsidePath` exempts any token containing `://` — a URL is not
a filesystem path. The consequence was that `curl http://169.254.169.254/…`
named no offending path, classified as plain `KindExec`, and auto-ran under
`auto-safe` and `never-escalate`, walking straight around the SSRF guard
`web_fetch` implements carefully. The invariant Classify states for
web_fetch ("approving a fetch grants reaching a PUBLIC url, never an
internal one") held for exactly one of the two egress paths.

`run_command`/`run_background` now classify as `KindEscalation` when the
command names an internal host (IP literal in a loopback/private/link-local/
CGNAT range, or a well-known internal name such as `metadata.google.internal`).

**The bar is parity with `web_fetch`, not "no egress from exec".** web_fetch
itself permits public URLs, so escalating every outbound command would be
STRICTER than the path being restored parity with, and under
`never-escalate` — which denies escalation outright — it would break
ordinary headless work (`git clone https://…`, `go mod download`). Public
egress stays `KindExec`; only internal egress escalates.

**Behaviour change:** under `never-escalate` a command naming an internal
address is now DENIED rather than run. Under `auto-safe` it prompts.
`prompt`/`strict` are unchanged (exec already prompted there).

**Known limit:** the check is LEXICAL. A DNS name resolving to an internal
address still walks through, where web_fetch catches it at dial time via
the resolved IP in `Dialer.Control`. Closing that for exec needs the parked
sandbox (§3a) or a resolving pre-check with its own TOCTOU window.

### `agent_spawn` item events for agent() calls (2026-07-27, agora#155)

`itemTypeForTool` had no case for the `agent` tool, so an `agent()` call fell
through `default:` to `ItemCommandExecution`. A subagent — the longest-running
thing a turn can do, and one that blocks the parent for the child's whole
lifetime when foreground — was therefore indistinguishable on the wire from
any other tool call. During agora#152 the operator's entire view of a
deadlocked child was the generic spinner, for 30+ minutes.

`contracts.ItemAgentSpawn` and `ItemWorkflowProgress` were already defined and
registered in the contracts known-items test with ZERO production emitters.
This wires the first of the two rather than inventing a shape.

**External contract note:** `item.started`/`item.completed` for an `agent`
call now carry `agent_spawn`, not `command_execution`, with agent-shaped
payloads (`{agent_type}` on start; `{agent_type, result, error}` on
completion) instead of `{command, output, error}`. A consumer matching agent
activity on `command_execution` must be updated. The child's final message
still round-trips as the tool result — it moved from the payload's `output`
field to `result`.

`ItemWorkflowProgress` remains unemitted: `agora workflow run` is a separate
CLI entry point and no tool exposes workflows to a live turn, so there is no
session surface for workflow progress to appear in yet.

## 10. Environment / housekeeping notes
- **`go.starlark.net`** (U14) must be added to the **go-gate proxy allowlist** for
  sovereign builds on croft/dMon. GitHub CI uses the public proxy, so CI is fine.
- The **v0-legacy TUI** (`internal/ui`) was retired at U15; **`internal/opclient`**
  was orphaned by the same cut and is **deleted** as of 2026-07-27 (agora#139).
  It was the last importer of the `github.com/CarriedWorldUniverse/nexus`
  module, which is dropped from `go.mod` with it — agora is no longer a
  cluster client in the build graph, not just in the README. The `v0-legacy`
  line remains the runnable reference per the U1 cut.
- `EvProvisioned` was registered in the `contracts` known-events test at U18 (a
  pre-existing gap).

## 11. turnengine tool-call item events (NEX-784 / Phase 2 U-C5): reads fold into command_execution; an unmatched Result is skipped
`internal/turnengine/sink.go` translates bridle `ToolCallStart`/`ToolCallResult`
into `item.started`/`item.completed`, keyed by the call ID so both share one
`item.seq` (a `map[string]toolCallState` under the sink's existing mutex,
recorded at Start and consumed once at Result). Two choices not spelled out by
`agora-spec-io.md`:
- **Tool name -> `contracts.ItemType`:** `run_command` -> `ItemCommandExecution`;
  `Write`/`Edit` (and their legacy `write_file`/`edit_file` spellings) -> `ItemFileChange`; `mcp__`-prefixed ->
  `ItemMCPToolCall`; **everything else (the read-only fs tools —
  `Read`/`ListDir`/`Glob`/`Grep` — and any unrecognized tool name) falls
  back to `ItemCommandExecution`**, with a synthesized `"<tool> <key arg>"`
  command summary (e.g. `Read hello.txt`) — there is no dedicated
  `ItemType` for read-only tool activity in the vocabulary yet, and
  `command_execution`'s shape (a readable command string) is the closest fit
  for v1. Args are decoded best-effort; a decode failure (or a tool with
  neither a `path` nor a `pattern` field) falls back to the bare tool name —
  never a panic (a model-supplied Args blob is untrusted input, unlike the
  locally-built payload structs `mustMarshal` panics on).
- **A `ToolCallResult` with no matching recorded `ToolCallStart` is SKIPPED**,
  not given a fresh seq. `ToolCallResult` carries no `Name`/`Args` of its own
  (see bridle's `events.go`), so there is no way to classify an orphan Result
  into the right `ItemType` or build its payload; bridle's own
  `executeToolCall` (run.go) always emits Start before Result for the same ID,
  so this is a defensive branch, not an expected path.
Payload shapes (local structs, `mustMarshal`'d, matching this file's own §5
convention for approval payloads):
- `command_execution`: started `{"command":"..."}`; completed
  `{"command":"...","output":"...","error":"..." }` (`error` omitted when
  empty). `output` unmarshals `ToolCallResult.Result` as a JSON string first
  (the toolrunner convention — see `surfacerunner.go`) and falls back to the
  raw JSON bytes on a non-string result.
- `file_change`: started `{"path":"..."}`; completed
  `{"path":"...","error":"..."}` — the full diff is heavy; path + status is
  the v1 shape, matching this file's own §5 `patch` note.
- `mcp_tool_call`: started `{"tool":"...","args":<raw Args>}`; completed
  `{"tool":"...","result":<raw Result>,"error":"..."}` — both Args and
  Result ride through unmodified (no schema to decode against here).

## 12. NEX-796 (event-time ts + turn_usage): usage also persists for interrupted turns; ts is captured, not clock-injected, at bridle-event granularity
`agora-spec-persistence.md` §1 describes `turn_usage` as recording "the
`turn.completed` usage payload" — that event is emitted only on a
successful turn. `internal/turnengine/manager.go`'s `persistTurn` is called
from BOTH the success path (`StopReasonModelDone`/`StopReasonMaxSteps`) and
the interrupted path (`StopReasonAborted`, NEX-798) — this unit appends a
`turn_usage` item from `persistTurn` unconditionally, so an interrupted
turn's PARTIAL usage (whatever `bridle.TurnResult.Usage` carries at abort)
is persisted too, not just a fully-completed turn's. Rationale: the spec's
own goal — "ccusage-style session/cost history is reconstructable from the
JSONL alone" — is better served by recording whatever usage actually
happened than by silently dropping an interrupted turn's cost; a provider
that bills per-token doesn't refund on interrupt. If this over-reads the
spec, dropping `turn_usage` on the aborted branch is a one-line revert
(`internal/turnengine/manager.go`'s two `persistTurn` call sites).

Separately: the ts fix stamps each `ThreadItem` with the REAL bridle event
time (`turnSink` now records `ToolCallStart`/`ToolCallResult`/`TurnDone`
wall-clock timestamps as they're `Emit`ted, and `persistTurn` reads them
back by tool-call-ID after `RunTurn` returns) rather than sourcing ts from
a Manager-level clock sampled once per item-creation call in a tight loop
(which would have reproduced the exact "everything looks like it happened
at the SAME instant" bug this ticket fixes, just moved one layer down). A
new `Manager.clock`/`WithClock` seam exists for test determinism, but the
authoritative ts values come from the sink's live event capture, not from
calling the clock repeatedly at persist time.

## 13. Lifecycle hooks (internal/hooks) wired into the live turn path — v1 scope narrowed on several axes
`internal/hooks` (the 10-event engine, built fully tested but unconsumed) is
now wired into `internal/turnengine` (`hookrunner.go` — discovery/trust/
dispatch — and `hooks_wire.go` — WHERE each event fires) and the in-process
launch path (`cmd/agora/inprocess.go`'s `DiscoverHooks`/`WithHooks`). Fired:
`PreToolUse`/`PermissionRequest` (`approval.go`'s `beforeToolCall`, before
and inside the approval decision respectively), `PostToolUse`
(`RegisterAfterToolCall` on every harness), `SessionStart` (once per
`Manager.Run`), `UserPromptSubmit` (once per turn-starting `user_message`),
`Stop` (once per successfully-completed turn). NOT fired (spec scope note,
unchanged): `PreCompact`/`PostCompact` (no compaction engine yet),
`SubagentStart`/`SubagentStop` (no subagent turn path wired to this
Manager). `internal/hooks` itself is UNCHANGED — every deviation below is in
the NEW wiring code, not the engine package.

- **Trust/enable state has no home in a real config file yet.** Spec §4.4
  says state is read from "the User(+session) layer" of the main TOML
  config — but no central agora TOML config loader exists in this codebase.
  This unit persists the same information as a JSON sidecar,
  `~/.agora/hooks-state.json` (`hookrunner.go`'s `hookStateEntry`/
  `loadHookState`), keyed by the SAME `PositionalKey()` scheme the spec
  defines. No `/hooks` TUI verb exists yet to WRITE this file (that's a
  separate, explicitly out-of-scope unit) — until one exists, trusting a
  hook is a manual JSON edit. A fresh install with no hooks-state.json
  means every discovered handler resolves `Untrusted` and never runs
  (`trust.go`'s existing fail-closed default, unmodified) — this is
  intentional, not a bug: "clone a repo, hooks don't silently execute."
- **Discovery covers User + Project layers only.** Managed (broker-pushed)
  and Plugin layers are reserved slots per spec §4.3/§0 — no broker-push
  mechanism or plugin-source discovery exists to populate them. Git
  worktree hook-discovery redirection (§4.2's "codex nicety") is not built.
- **Hook process lifetime is detached from the turn/session `context.Context`**
  (`hooks_wire.go`'s `fire`, dispatches on `context.Background()` rather
  than the caller's turn-scoped ctx). A hook's ONLY lifetime bound is its
  own configured `Timeout` (spec §1.4, default 600s/floor 1s) — a turn
  ending (and cancelling its `turnCtx`) does not kill an in-flight hook
  process. This matters most for `async: true` handlers: tying them to
  `turnCtx` would kill an async hook the instant `Manager.Run` reaps the
  turn's terminal event, silently downgrading "doesn't block the turn" to
  "doesn't survive the turn either" — defeating async for anything slower
  than the turn itself (a notification, an audit upload...). Sync handlers
  are affected too (no longer cut short by an unrelated turn interrupt),
  which is judged the more honest reading of "a hook is an external,
  timeout-bound process," not a turn-lifetime-bound one.
- **`permission_mode` on hook stdin is a stub, always `"default"`.**
  `agora-spec-approvals.md` §3 documents this field as derived from a
  profile/preset resolution (`never-escalate`/`acceptEdits`/etc.) this
  engine doesn't have yet (`defaultPolicy()` is the only `PolicySet` wired
  in) and explicitly report-only ("hooks never *configure* via this
  field") — nothing in this engine reads it back, so the stub cannot
  silently loosen or tighten any decision. A real profile/preset unit
  should replace `HookRunner.common`'s hardcoded value.
- **`SessionStart`/`UserPromptSubmit` `continue:false`/block is logged, not
  enforced**, beyond what each event's OWN wiring point can naturally do:
  `UserPromptSubmit` CAN refuse to start the turn (`Run`'s `InUserMessage`
  case — implemented) but `SessionStart`'s block/stop has no session-abort
  mechanism to hook into (`Manager.Run` always proceeds once started) — a
  stderr warning fires instead of silently doing nothing.
  `additionalContext` from both folds into the nearest existing injection
  point this engine has (`m.appendSystemPrompt` for SessionStart, a
  `"\n\n[hook context]\n"`-prefixed suffix on the turn's prompt for
  UserPromptSubmit) rather than a dedicated "developer message" channel,
  which doesn't exist in this engine yet.
- **`Stop` fires only on a genuinely successful turn**
  (`StopReasonModelDone`/`StopReasonMaxSteps`), never on an aborted or
  errored turn — spec's Stop/`stop_hook_active` continuation-loop machinery
  (§2.9/2.10) presumes a real model response happened, which an interrupted
  or errored turn never produced. A `Looped` (continuation-requested)
  outcome is logged, not honored: `runOneTurn` has already returned its
  terminal result by the point `fireStop` runs, and re-entering `RunTurn`
  for a continuation round is out of this unit's scope.
- **`PostToolUse` block feedback folds into the tool_result's `Err` field**
  (`bridle.AfterToolCallCtx.Result.Err`, which `bridle`'s `run.go` already
  uses verbatim to build the model-facing `tool_result` message) rather
  than a separate feedback channel — the closest existing mutable surface
  this hook point has. `continue:false` maps to `bridle.HookAbort`, which
  ends the turn the same way an interrupt does (`StopReasonAborted` ->
  `turn.failed{interrupted:true}`); the hook's own stop reason isn't
  separately surfaced past that.

## Open follow-up tickets
- **NEX-764** — nexus-side dispatch `needs_input` consumer (pod `blocked:
  needs-input` route to context/operator + re-dispatch; a nexus change, not agora).
