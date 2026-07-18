# Spec ↔ code deviations (U0–U18 build)

The `agora-spec-*.md` files are the **design of record**. The U0–U18 build tracked
them closely, but in a handful of places the shipped code deliberately differs
from, defers, or gets stricter than the spec text. This file is the ledger of
those gaps so "what we designed" vs "what we built" is written down rather than
living only in PR bodies. Keep it current as the specs and code converge.

Status as of main `7ef1b47` (2026-07-17), agora epic NEX-743, units U0–U18 merged.

## 1. The real turn engine is unbuilt (the largest deliberate gap)
U0–U18 build the **seams + their assembly**. The model-driven turn engine — model
calls, the tool loop, and interactive approval/question *resume* (an
`approval_response` continuing an in-flight turn; a blocking question parking then
resuming) — is intentionally **out of scope**. `io.ScriptedEngine` and the U18
`conformance/flow_engine.go` (a scripted engine with input-await points that calls
the real seams) stand in for it. Wherever a spec says "the engine does X"
interactively, that is the future turn-engine's job. It is gated on **U6 bridle**
gap-closure (separate repo, still open).

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

## Open follow-up tickets
- **NEX-764** — nexus-side dispatch `needs_input` consumer (pod `blocked:
  needs-input` route to context/operator + re-dispatch; a nexus change, not agora).
