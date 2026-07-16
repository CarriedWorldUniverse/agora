# agora spec set — canonical

This directory is the **canonical** home of the agora design specs (copied in at
build unit U1). The design phase authored them off-git in croft `~`; from U1
onward the in-repo copy is the source of truth and design iteration happens here.

- `agora-spec.md` — the index: profiles, identity, config layering, the chapter table, build order.
- `agora-spec-build.md` — the one-shot build decomposition (units, waves, DoD, the review gate).
- `agora-spec-<area>.md` — one chapter per seam (io, persistence, approvals, planning-questions, prompt, context, context-curation, bridle, mcp, hooks, subagents, workflows, tui, remote, skills, memory).

The compiled form of the seams these chapters define lives in `/contracts`
(unit U0) — the Go types every unit builds against. Where a chapter and the
contracts package disagree, that is a bug to reconcile, not a choice.
