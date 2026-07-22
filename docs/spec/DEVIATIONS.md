# Spec ↔ code deviations (U0–U18 build)

The `agora-spec-*.md` files are the **design of record**. The U0–U18 build tracked
them closely, but in a handful of places the shipped code deliberately differs
from, defers, or gets stricter than the spec text. This file is the ledger of
those gaps so "what we designed" vs "what we built" is written down rather than
living only in PR bodies. Keep it current as the specs and code converge.

Status as of main `7ef1b47` (2026-07-17), agora epic NEX-743, units U0–U18 merged.

## 1. The turn engine is BUILT (Phase 2) — plan/question special-casing + MCP wiring remain
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

Still open (the remainder of the original gap):
- **plan/question special-casing** — no plan/question tools exist on the
  fs/exec surface; `KindQuestion`/`KindPlan` sit in the policy table only
  for completeness, and `question_response` input is currently a no-op
  (turnengine/manager.go). A blocking question parking then resuming a turn
  remains future work.
- **MCP wiring** — `internal/mcp` (U8) is still unconsumed: the turn path
  wires a nil MCPSource and leaves `TurnRequest.MCP` unset (claudesdk
  `SupportsMCP=false` — MCP tools ride in `Tools`, not MCP;
  turnengine/manager.go). `mcp__`-prefixed calls already classify into
  `ItemMCPToolCall` wire events (sink.go), so the event shape is ready for
  the wiring ticket.

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
  tools) → `{ "detail": "..." }`: the path for read_file/list_dir, the
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
- `Manager.Compact` is a **documented no-op** (curation runs continuously inside
  `Assemble`, per context-spec §1) — it fires the Pre/PostCompact hooks and reports
  the estimate, not additional curation work.

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

## 10. Environment / housekeeping notes
- **`go.starlark.net`** (U14) must be added to the **go-gate proxy allowlist** for
  sovereign builds on croft/dMon. GitHub CI uses the public proxy, so CI is fine.
- The **v0-legacy TUI** (`internal/ui`) was retired at U15; **`internal/opclient`**
  is now orphaned (nothing imports it) pending a cleanup pass.
- `EvProvisioned` was registered in the `contracts` known-events test at U18 (a
  pre-existing gap).

## 11. turnengine tool-call item events (NEX-784 / Phase 2 U-C5): reads fold into command_execution; an unmatched Result is skipped
`internal/turnengine/sink.go` translates bridle `ToolCallStart`/`ToolCallResult`
into `item.started`/`item.completed`, keyed by the call ID so both share one
`item.seq` (a `map[string]toolCallState` under the sink's existing mutex,
recorded at Start and consumed once at Result). Two choices not spelled out by
`agora-spec-io.md`:
- **Tool name -> `contracts.ItemType`:** `run_command` -> `ItemCommandExecution`;
  `write_file`/`edit_file` -> `ItemFileChange`; `mcp__`-prefixed ->
  `ItemMCPToolCall`; **everything else (the read-only fs tools —
  `read_file`/`list_dir`/`glob`/`grep` — and any unrecognized tool name) falls
  back to `ItemCommandExecution`**, with a synthesized `"<tool> <key arg>"`
  command summary (e.g. `read_file hello.txt`) — there is no dedicated
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

## Open follow-up tickets
- **NEX-764** — nexus-side dispatch `needs_input` consumer (pod `blocked:
  needs-input` route to context/operator + re-dispatch; a nexus change, not agora).
