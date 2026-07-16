# agora spec — build plan (one-shot decomposition)

2026-07-16, operator intent: **the spec set is the build brief** — once planned, agora builds mainly "one shot" via subagents/workflows, with acceptance gates doing the judging. This chapter is the decomposition: units, dependency edges, observable DoD per unit, model routing, and what deliberately stays interactive.

## 0. Ground rules

1. **Contracts as code before fan-out.** Unit U0 translates the prose seams into a compiling `contracts` package + JSON schemas ONCE, centrally. Parallel builders import compiled types; divergent spec readings become compile errors, not integration surprises.
2. **Observable DoD only** (the observable-criteria standard — acceptance checkable from artifact + evidence, never from the agent's word): every unit's acceptance is `-race` tests pass, golden fixtures match, conformance green — a human or a gate can verify it without trusting a narrative. The acceptance gates (judge-the-diff + test-evidence) enforce this at PR time.
3. **Specs travel into the repo** at U1 (`docs/spec/` — this directory copied, then repo copy = canonical). Off-git-in-`~` was right for the design phase; pod builders can't read croft's home. Design iteration continues in-repo from then on.
4. **Old code**: the repo's existing TUI (WS chat panel on claudecode) freezes on branch `v0-legacy` at U1 — runnable reference until the new TUI reaches parity; `main` restarts with the new skeleton. Nothing deleted.
5. **Interactive exceptions** (never one-shot; their machinery IS one-shot, their content/tuning is not): the core prompt TEXT (models × core eval, prompt §6), the TUI FEEL pass (operator dogfood after U16), context-curation TUNING (knobs + synthetic task, curation §8), and the workflow-creator/agent-def content.
6. **Fabric = hybrid**: bounded units → the nexus dispatch pool (audit trail, parallelism, gates); seams/contracts/reviews/integration → shadow in-session (orchestrator/workflow). Model routing per docs/network/MODEL-SELECTOR.md: cheapest-that-clears — bounded/mechanical → local/metered tier; contracts, U12 algorithm, all review passes → frontier.
7. **TDD is the merge condition** (operator, 2026-07-16), at two levels: (a) *architecture* — U0 authors the golden fixtures, contract-test tables, and U18 flow skeletons RED, before implementations exist; a unit's DoD is making its slice green; (b) *brief* — every brief is test-first, and any behavior fix lands with its failing test proven failing pre-fix in the same PR (the li1 discipline). Gates enforce the evidence (`ACCEPTANCE_REQUIRE_TEST_DIFF` on for this repo); shadow's pre-merge review owns test QUALITY (teethless tests are a reviewer catch, not a gate catch).
7a. **Routing envelope** (operator, 2026-07-16: "use z.ai and claude as you see fit"; deepseek v4/v4-flash "cheap enough to use freely"): pool briefs seed at Ornith (free local), escalating — or seeding directly, shadow's per-brief judgment — across the freely-usable tier: **deepseek-v4-flash/v4-pro** (metered but pennies-per-task; v4-flash for bounded, v4-pro for complex — verify the post-eval key rotation before first use), **GLM-4.6** (coding-plan subscription), **claude-code** (subscription, low-effort-first per the grid inversion). Codex excluded (lane down, NEX-566). Wave-2+ hard units may seed directly at the stronger tiers rather than burning an Ornith round. U0 + all reviews = frontier (shadow's session model). Epic: **NEX-743**; stories cut per wave.
8. **Merge authority — the review gate IS the authority** (operator, 2026-07-16, superseding the earlier scoped/self-merge carve-outs): "you don't need to authorize each phase in this build — if the security and the reviewers pass, then merge and continue." So: **any unit whose review gate (rule 9) is green — security scan + both adversarial reviewers pass, confirmed findings fixed, delta re-review clean — shadow merges autonomously and proceeds to the next**, INCLUDING shadow-authored units (the self-merge carve-out is lifted for this build). No per-phase approval stop. Active merges (poll CI, squash, delete branch). **Inform, don't gate:** shadow still posts wave-boundary digests and flags the high-blast-radius units (U7 approvals, U16 remote/MLE) to the operator AT merge time — a heads-up window, not a block. A genuine spec ambiguity or a finding shadow cannot resolve is still a real question up the ladder (planning-questions §5) — that is a legitimate pause, distinct from a merge-authorization pause, which no longer exists.
9. **THE REVIEW GATE — standing, every unit, no exceptions** (operator, 2026-07-16, after the U0 pass caught cross-model-confirmed attribution forgery + a toothless test that gates alone missed). Every build deliverable clears this pipeline before it is merge-eligible: **spec/plan → build → security scan → adversarial review (2 different models) → fix confirmed findings (TDD) → shadow synthesis/verify → merge.** Details:
   - **security scan** = a `security-validator` pass (design-level for contracts/wire types; exploit-path for runtime units).
   - **adversarial review = TWO reviewers on DIFFERENT model families**, both briefed to REFUTE (findings need file:line; CONFIRMED vs PLAUSIBLE). At least one reviewer differs from the *builder's* model — never let a model bless its own family's output unchallenged. Proven lanes: a Sonnet subagent + a non-Anthropic model (DeepSeek-v4-pro / GLM) driven through the litellm Anthropic passthrough (`claude-ornith` wrapper generalizes to any litellm route). Cost is pennies/subscription per the routing envelope — diversity is the point, not redundancy.
   - **confirmed findings** are fixed TDD-first (failing test proven, then fix); **PLAUSIBLE** ones shadow verifies before acting; disputed spec-fidelity calls check against the spec text, not the reviewer's assertion.
   - **after a non-trivial fix, re-review the delta** with a fresh reviewer (the fix can introduce new holes — the U0 fix got a 4th verification pass).
   - **depth scales with surface** (a 3-file mechanical unit gets a fast pass; a seam/security unit gets the deep treatment) — but the pipeline runs on ALL of them; scaling depth ≠ skipping stages.
   - shadow synthesizes the passes, triages, and only then proposes merge (or sends to the operator per rule 8). This is the [[feedback_build_review_gate]] discipline applied to agora.

## 1. Units

Legend: deps ⇒ must be merged first. All units include their own tests; "golden" = fixture files under `testdata/`, checked in.

| unit | scope (spec §) | deps | observable DoD |
|---|---|---|---|
| **U0 contracts** | Go interfaces + wire schemas: io events/inputs, ThreadStore, ContextManager, approvals triple + kinds, ModelInfo/Stream (bridle-facing), tool-registry surface, question/plan payloads, provision message | — | package compiles; JSON schemas validate the golden fixture set; frontier review maps every exported symbol to its spec § |
| **U1 skeleton** | repo layout (cmd/, internal/<pkg-per-unit>), CI (lint+test+`-race`), specs → `docs/spec/`, `v0-legacy` branch cut, empty packages compile | U0 | CI green on main; goldens harness runs |
| **U2 io** | pipe mode + session protocol + daemon multi-attach, fan-out, first-answer-wins, presence, replay (io spec) | U0,U1 | golden JSONL event streams for scripted turns (stub engine); multi-attach fan-out + replay tests; exit codes |
| **U3 persistence** | JSONL + SQLite mirror + ThreadStore Local/Mem, fork, rebuild-index (persistence spec) | U0,U1 | replay/fork/resume round-trip tests; rebuild-index property test (mirror ≡ derived); crash-safety at fsync boundaries |
| **U4 prompt compose** | segments + role map, core package resolve (override/variants/base_version), deterministic render, dialect knobs, `agora prompt` verbs (compile may stub) (prompt spec) | U0,U1 | golden renders per (core,profile,model) matrix; byte-stability test (same inputs ⇒ same bytes); drift-warning + check/rebase behaviors |
| **U5 skills + AGENTS.md** | discovery roots + guards, catalog + budget fitting, $mention, invocation, implicit detection; AGENTS.md discovery/merge (skills spec; subagents §6) | U1 | fixture-tree discovery tests incl. traversal guards + precedence; budget-fitting cases from skills §3.2; lenient-YAML repair cases |
| **U6 bridle gap closure** *(bridle repo)* | the §5 checklist: registry metadata (context_window, pricing, prompt block, `system_prompt_mode`), role translation, error taxonomy, structured forcing, effort map, cancellation latency | — (parallel) | one ticket per checklist item; env-gated live (L2) tests per lane where applicable — each closed BEFORE its dependent agora unit ships |
| **U7 approvals engine** | kinds, policy sets, presets, decision pipeline, scoped allows, audit lines (approvals spec) | U0,U1 | table-driven preset × kind matrix incl. question `convert†` exception; scope persistence tests |
| **U8 MCP manager + tools** | server config + startup + catalog cache + naming + OAuth; deferral/tool_search; native family registration; fs-watcher (mcp spec §1–5a; wasm §1a = v1.1) | U0,U1,U7 | stub-server startup matrix (required/timeout/auth); cache-key tests; tool_search select+keyword goldens; watcher staleness tests incl. mtime-sweep fallback |
| **U9 hooks engine** | 10 events, per-event I/O contracts, layering, trust hashes, async (hooks spec) | U0,U1,U7 | per-event contract tests straight from hooks §2 (they are written as test tables); trust/layer matrix; async non-blocking proof |
| **U10 subagents** | agent defs, `agent()` tool, graph store, cancellation matrix, continuation (subagents spec) | U2,U3,U7 | graph persistence + BFS tests; cancellation matrix §2a; schema-forced structured output retry test |
| **U11 planning + questions** | `plan` tool/item + posture overlay + gate (submit/exit-authority/open-questions invariant); `question` tool + ladder (park / needs-input conversion / bubble); `waiting-on-answer` durable state (planning-questions spec) | U2,U3,U7 | ladder matrix tests (context × blocking); gate refuses allow while open_questions ≠ ∅; park survives daemon restart; pod call ⇒ `blocked: needs-input` golden |
| **U12 context** | ContextManager seam + curation algorithm: ledger, two-layer budget, hysteresis, staleness, re-admission (+SpanIndexer seam, line-window fallback) (context + curation specs) | U0,U3,U8(watcher) | §7 contract-compliance tests, one per contract; cwlog-style benchmark harness runs; supersession/staleness goldens. TUNING = interactive, after |
| **U13 memory** | store + `memory.*` family + index injection (memory spec) | U1,U4 | atomic index-update race test; budget truncation cases |
| **U14 workflows engine** | starlark host API incl. `ctx.question`/`ctx.approval`, scheduler + caps, journal/resume, run parking (workflows spec) | U10,U11 | journal resume property tests (edited-tail-only reruns); frozen-clock determinism; answer-replay golden; parked-run recovery via replay-and-re-ask |
| **U15 TUI** | inline viewport, cells, two-region streaming, composer (+`%` override), approval modal + question card + plan modal, slash set, diff render (tui spec) | U2,U7,U11 | streaming invariant tests (newline gating, table holdback, append-only); snapshot renders; modal option→(decision,scope) mapping tests. FEEL pass = interactive, after |
| **U16 remote/MLE** | classical IK + pairing + capabilities + device mgmt + queue/timeout (question park exempt) + reconnect/replay (remote spec §1–5, §8–9) | U2,U7 | handshake × enrollment matrix (unenrolled/revoked refused); capability enforcement matrix; gap-replay tests. Interchange + browser-wasm + push = v1.1 |
| **U17 pod mode + provision** | `--pod` blank boot, atomic provision/deprovision, dispatch-as-controller, needs-input surfacing to the broker (remote §6a; planning-questions §8) | U11,U16 | provision atomicity (apply-all-or-reject) tests; e2e stub-broker drive: provision → turn → blocked:needs-input round-trip |
| **U18 conformance e2e** | boots real daemon; drives pipe + ws clients through golden FLOWS: turn, approval, question park/resume, plan gate, compaction/curation events, resume/fork | starts at U2, grows with every unit | the flow suite green in CI = the definition of "assembled"; every later unit adds its flow here in the same PR |

## 2. Sequencing (fan-out shape)

```
U0 → U1 → ┬ U2 ─┬ U10 ─ U14
          ├ U3 ─┤ U11 ─ U17          U6 (bridle) runs parallel from day 0
          ├ U4 ─┼ U12                U18 grows continuously from U2
          ├ U5  ├ U15
          ├ U7 ─┼ U9, U8, U16
          └ U13 (after U4)
```

Three waves after the skeleton: **wave 1** = U2,U3,U4,U5,U7 (pure-seam consumers, maximally parallel); **wave 2** = U8,U9,U10,U11,U12,U13,U16; **wave 3** = U14,U15,U17. Review + integration pass (frontier) closes each wave before the next fans out; U18 gates the whole thing continuously.

## 3. What the planner (shadow) keeps

U0 authorship; wave boundaries + integration reviews; the interactive units (§0.5); brief-cutting per WORKITEM-BRIEFS (tracer-bullet slices inside big units — U8/U15/U16 are 2–4 briefs each, split at their internal seams); the nexus-side `needs_input` dispatch ticket (planning-questions §8) filed when U17 starts.

## 4. Done means

`agora daemon` + TUI + pipe mode running the dev profile end-to-end on croft/dMon (U18 suite green), a pod provisioned and driven by a stub dispatch controller, and the old `v0-legacy` agora retired from daily use. Post-v1 line (already spec'd, not in this plan): wasm transport, interchange relay + push, QuestionAsked hook, durable parked runs, PQ suite, browser device keys.
