# agora spec — self-learning: verified knowledge, prediction checks, refactor-compaction

Design source: the Schema harness / "Executable World Models for ARC-AGI-3" line of work
(arXiv 2605.05138). Its transferable core: **knowledge earns trust by verification against
recorded reality, is compressed by refactoring under a correctness gate, and the harness —
not the model — decides when to reflect.** agora already applies that standard to
acceptance (observable-criteria) and to reviews (mock the producer's real output); this
chapter applies it to the harness's own memory and learning loop.

Principles (contract-level):
1. A learned fact that CAN carry an executable check MUST carry one; facts without checks
   are hypotheses and render as such.
2. Prediction/reality mismatches are blocking feedback to the model, never silently absorbed.
3. Compression (compaction, reconciliation) must prove the compressed form still answers
   what the history established — refactor, don't summarize.
4. Reflection is harness-triggered at junctures (stall, repeated failure), not left to the
   model's initiative.
Caveat carried from the source work: repos are messier than 64×64 deterministic games —
checks must be cheap and deterministic (grep/stat/single-test), everything else is
human-verify, per docs/network/OBSERVABLE-CRITERIA.md's discipline.

## U-SL1: verified facts (ctxmap fact schema + check runner)

Extend the ctxmap fact shape (bridle ctxmap; extraction path
internal/turnengine/ctxextract.go) with optional `verify: {cmd, expect_substring | expect_exit}`.
- Extraction prompts gain one rule: when a fact is mechanically checkable in-repo (a build
  flag, a path, a symbol, an invariant), emit the cheapest deterministic check command.
- A check runner (harness side, sandboxed through the exec family + approval policy,
  KindRead-equivalent for read-only cmds) re-runs checks lazily: on recall into the working
  set, and at most once per session per fact. Pass → fact stays asserted, `verified_at`
  bumped. Fail → fact DEMOTED to hypothesis (rendered with an explicit "unverified —
  last check failed" marker), never silently dropped (history is append-only).
- No check present → rendered as hypothesis-grade ("reported, unverified").
Acceptance: extraction e2e (existing TestManager_ExtractionLandsInWorkingMemory pattern)
where a fact with a passing check renders asserted and the SAME fact with a failing check
renders demoted; check commands run through the approval gate (policy-deny test blocks them);
a fact with no check renders with the hypothesis marker.

## U-SL2: prediction-check on mutating tool calls (mismatch artifacts)

The Schema loop's parallel-simulation check, adapted to tools: the model states expected
outcome; the harness compares and injects divergence as blocking feedback.
- Tool schema addition: mutating families (exec, patch) accept optional `expect`
  (short string: what success looks like — exit 0, substring, "tests pass").
- PostToolUse (built-in harness handler on the existing hooks/afterToolCall path —
  internal/turnengine/hooks_wire.go precedent; NOT a user hooks.json): compare result
  against `expect`; on mismatch, append a MISMATCH ARTIFACT to the tool result — verbatim
  expected-vs-actual, no interpretation — so the model must confront it in-context.
- Persist mismatches as thread items (new item type or tool_result payload field) — they are
  the harness's training signal and the operator's audit trail.
- Prompt core (internal/prompt builtin core.md, tool-discipline section): instruct the model
  to set `expect` on consequential mutations. Never mandatory — absent `expect` = no check.
Acceptance: fake-provider turn scripting an exec call with expect that the scripted result
violates → the NEXT model request's tool result carries the mismatch artifact verbatim
(captured request assert); matching expect adds nothing (byte-identical result); mismatch
persisted on the thread.

## U-SL3: compaction-as-refactor (verified compression)

Upgrade ctxmgr compaction (internal/turnengine/ctxcuration.go runCompactionEpisode /
ctxmgr Compact) and ctxmap reconciliation from summarize to refactor-under-gate:
- The compaction model call is prompted to REWRITE the evictable span into fewer, more
  general statements (merge duplicates, replace special cases with rules) — the MDL proxy.
- Gate: a verifier pass (second model call, judge-style — the dormant JudgePair seam is the
  natural home on the ctxmap side) checks the refactored form against a sampled set of
  claims from the original span: any contradiction or lost load-bearing fact → reject, fall
  back to the current plain summary. Verified facts (U-SL1) are hot-set immune: never
  compacted away while their checks pass.
- Budget: the whole episode ≤ 2 model calls; rejection is cheap and non-fatal.
Acceptance: compaction fixture where two redundant facts + one special case compress into
fewer statements AND a poisoned refactor (fixture verifier double rejecting) falls back to
summary; compaction marker item + events unchanged (existing tests stay green).

## U-SL4: stall-triggered reflection (harness junctures)

A small turnengine watchdog: same exec command failing N times (default 3) in one turn, or
the same file patched M times (default 4), injects ONE steer message: "stop — state your
model of why this is failing, verify it against the last N results before continuing."
Once per turn per trigger; off in headless/pod lanes (die-honestly contexts). Constants
overridable via ProfileConfig.
Acceptance: scripted turn with 3 identical failing execs → captured next-request carries the
injected steer exactly once; distinct commands never trigger; headless profile never fires.

## Patterns (no new plumbing — workflow library + skills, ticket-only)

- **Diverge-on-stuck**: a .star pattern spawning N children with deliberately different
  priors on a stuck problem + a judge stage (premature-commitment mitigation). Ships as a
  workflow example, exercised by a workflow e2e test.
- **Skill self-authoring with execution gate**: post-task distillation into a draft SKILL.md
  + verification script; admission = script passes + operator approval. Rides U-SL1's
  check-runner + the skills discovery roots. Spec'd fully when the first two SL units land.

## Build order

U-SL1 → U-SL2 (independent of SL1 but shares the check-runner sandboxing decisions) →
U-SL4 (small, anytime) → U-SL3 (needs the judge seam warm). Structured output
(agora-spec-structured-output.md) is a force multiplier for SL1/SL3 verdicts — build it first
or in parallel. Standing rules apply (CGO=0, gofmt, Windows-safe tests, observable acceptance).
