# agora spec — TUI (lean, Go/bubbletea)

Extracted 2026-07-15 from codex-rs/tui (~366 files, ~220k lines — we take the interaction design, not the code). Target: a lean Go TUI, bubbletea or direct tcell, speaking to the agora core over the funnel session seam.

## 0. The one architectural idea to copy (non-negotiable)

**Chat history lives in the terminal's own scrollback, not in a widget.** Codex runs an *inline viewport* (never alternate screen for the main flow): finalized content is *printed* above a small persistent bottom region via escape sequences; the app only ever redraws (1) the single in-flight "active cell" and (2) the bottom pane. Consequences: native mouse-wheel scroll, native copy/paste, tiny redraw surface. Alternate screen is used only for full-page overlays (transcript pager, pickers).

Bubbletea mapping: print finalized cells with `tea.Println`-style passthrough; keep the composer + status row as the only managed region. Do NOT put the transcript in a `viewport.Model`.

Accepted tradeoff: resize-reflow of old scrollback is imperfect (codex spends serious code on scroll-region reflow; skip it — terminals soft-wrap acceptably).

## 1. Cell model

Transcript = append-only list of finalized cells (already in scrollback) + exactly one mutable **active cell**. Cell kinds for v1: user message, agent message (markdown), reasoning (collapsed/dim), exec (command + capped streaming output, e.g. last ~20 lines while active), diff/patch, approval-decision notice, session header. Each cell renders to lines at a given width; finalization = print + forget.

## 2. Streaming: two-region model

Per stream, partition rendered markdown into a **stable region** (complete lines, committed to scrollback via a periodic commit tick — N lines per tick, N adapting to queue pressure for catch-up vs smooth reveal) and a **mutable tail** (re-rendered in the active cell every delta).

- **Newline-gating**: deltas accumulate; only complete lines are eligible to render/commit — half-formed markdown never flashes.
- **Table holdback**: once a pipe-table header+delimiter is detected, everything from the header onward stays in the mutable tail until finalize (a new row can reshape all columns).
- Invariants: raw source append-only; tail starts exactly at the stable boundary; stable lines only grow.
- v1 simplification: commit each complete line immediately (skip adaptive chunking) — still smooth.

## 3. Approval UX (the trust loop)

Approval requests (canonical kinds, agora-spec-approvals §1: `exec` / `patch` / `escalation` / `mcp_tool` / `question` / `plan` / `gate`) render as a **modal list-select view that replaces the composer** in the bottom pane. Body shows the highlighted command or the diff. Requests **queue** and interleave with streaming; shown in order. Kind-specific bodies: **`question`** renders as a question card (options / multi-select / free-text — answerable with `interactive` capability, not the allow/deny modal); **`plan`** renders the plan artifact + any unresolved `open_questions`, and the allow option stays disabled while questions remain (planning-questions §3 — answer them first, in the card or conversationally).

v1 options (the essence): **approve once / approve for session / deny-and-tell-the-agent-what-to-do-differently** — the deny-with-feedback option routes back to the composer with focus so you type guidance. Every exit is an explicit decision (Esc = explicit deny/cancel, never silent). Decisions get recorded as a small history cell. Later: approve-for-prefix ("don't ask again for commands starting with `X`"), host allow/block for network, per-turn permission grants.

## 4. Composer

v1 must-haves:
- Multiline textarea; Enter submits (config for newline key).
- **Slash-command menu**: leading `/` opens filtered list; completed command becomes an atomic (non-editable) token.
- **@-file mention**: `@` opens async fuzzy file search; completion inserts atomic element.
- **$skill mention**: `$` opens skill picker (from the skills catalog).
- **Large paste as placeholder**: big pastes collapse to `[Pasted Content N chars]` element, expanded at submit.
- **Up/Down history** (persistent cross-session text history merged with in-session full-fidelity entries).
- **Queue-while-running**: messages typed mid-turn queue (with a preview row above the composer); submit-on-idle. Steering variant later.

Defer: Vim mode, Ctrl+R incremental history search, image paste, backtrack (double-Esc → fork thread before an earlier prompt and re-edit it — lovely, later), paste-burst reconstruction for broken Windows terminals.

### 4a. One-shot model/effort override (per-message, operator-requested)

Three coexisting levels: session default (`/model`), workflow per-stage routing (workflows spec §2a), and this — a **message-scoped override** that applies to the next turn only, then reverts.

- **Syntax**: message starts with `%alias` or `%alias:effort` — `%frontier:high fix this race condition`. `%` at message start opens a picker popup (same pattern as `/` for commands, `@` for files): aliases from bridle's registry + raw model ids, tab-complete; `:` then offers the full effort ladder `low|medium|high|xhigh|max` (default is `high` — see feedback-effort-prefer-high; xhigh/max are the opt-in tiers this override exists to reach). Effort alone is `%:high` (keep current model, raise effort). The completed directive becomes an atomic token in the composer, visually distinct, deletable as a unit.
- **Scope**: exactly one turn — including any subagents that inherit (they follow normal inheritance from the overridden turn). Status row shows the override while the turn runs. No sticky variant: sticky = `/model`.
- **Feedback**: the finalized user cell records the override (`[frontier:high]` chip) so transcripts show what ran where.
- **Headless parity**: `agora exec --model <alias> --effort <tier>`; same alias resolution.
- Unresolvable alias: inline composer error before submit, never a failed turn.

Rationale for `%`: single unshifted-adjacent char, unused by the composer (`/` commands, `@` files, `$` skills, `!` shell-passthrough reserved), and reads as "mode". Cheap to change if it feels wrong in practice.

## 5. Status & awareness

- One-line **status row** above composer while busy: spinner + elapsed + "Esc to interrupt" + short context. Keep it to one line.
- **Context-remaining %**: track input/cached/output tokens; percent of effective window remaining (codex subtracts a ~12k baseline). Show in footer and `/status`.
- Footer: model, permission mode, git branch (configurable later).
- Defer: terminal title (OSC), desktop notifications (OSC 9/BEL on turn-complete-while-unfocused — cheap, do early if easy), configurable statusline.

## 6. Slash commands — v1 set

`/model` (session default; one-shot = `%` prefix, §4a) `/plan` (planning posture — exit is operator-authorized via the plan gate, planning-questions §2–3) `/orchestrate` `/workflow <name>` (run/author workflows) `/cd <dir>` (change working dir) `/fork` (branch the thread) (planner mode toggle — subagents spec §5; footer badge when on) `/review` `/diff` (git diff pager) `/compact` `/new` `/resume` `/init` (create AGENTS.md) `/clear` `/copy` (last response as markdown) `/status` `/skills` `/mcp` `/hooks` `/quit`. Metadata per command: inline-args?, available-during-task?, visible?. Menu order = frequency, not alphabetical.

Full codex list (45+) in the extraction if needed; the rest is settings-pickers and feature surface.

### 6a. Slash-command containment (NEX-795, session-log finding 2026-07-20)

**No slash-prefixed input ever reaches the model.** The composer intercepts ANY
input starting with `/`: known commands dispatch; unknown produce a LOCAL error
with a nearest-command suggestion. Rationale (measured, thread
agora-6dac2f837e54): unintercepted `/mode`, `/eit`, `/glm`, `/modek` fell
through as user messages and the model ROLE-PLAYED a CLI error ("Unknown
command: /mode. Did you mean /model?") — fake-authoritative output that misled
the operator (and shadow), plus a billed turn per typo. Additionally:
`/<registry-name>` (e.g. `/kimi`, `/glm`) is sugar for `/model <name>` — the
shortcut form the operator typed instinctively. Escape hatch: a literal message
starting with `/` requires a leading space or `\/`.

## 7. Diff rendering

Right-aligned line numbers + gutter sign (+/-/space) + content; muted add/del background tints (theme-aware: dark `#213A2B`/`#4A221D`, light GitHub pastels; fall back to fg-only on ANSI-16). Hard-wrap long lines preserving style spans. Appears in the apply-patch approval modal and in finalized patch cells. Syntax highlighting inside diffs = polish, later (chroma if wanted).

## 8. Build order (minimum lovable TUI)

1. Inline viewport + scrollback printing (§0) — everything depends on it.
2. Cell model with one active cell (§1) + markdown render (glamour or custom).
3. Two-region streaming, newline-gated (§2).
4. Composer: slash menu, @-mention, Enter/multiline, history, paste placeholder, queue-while-running (§4).
5. Approval modal with queue + deny-with-feedback (§3).
6. Status row + context % (§5); essential slash set (§6); diff cells + /diff pager (§7).

Everything else in codex's 366 files is terminal-compat hardening, settings pickers, optional features (side conversations, plugins UI, pets), and tests — additive later, none of it load-bearing for daily-driver feel.
