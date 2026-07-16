# agora spec — system prompt composition

Settled conversationally with the operator 2026-07-16. One canonical **contract core** shared by every profile; per-model tuning is presentation, never semantics; the composed prompt is **regenerated state** (agora-spec-context contract: regenerated, not summarized), rebuilt deterministically per turn.

## 1. Segments — ordered, layer-owned

System-role prompt = the ordered concatenation below. Each segment has one owner; this chapter is normative for the ORDER; sibling specs own their fragment's mechanics.

| # | segment | owner / source | notes |
|---|---|---|---|
| 1 | **core contract** | built-in, versioned with the binary | §2 — identical for all profiles |
| 2 | **profile block** | profile definition | what this instance is *for*; active modes; register/voice guidance lives here, not in the core |
| 3 | **identity + persona** | identity dir (`~/.agora/identity/<name>/persona.md`; `SOUL.md` accepted import name) | id/kind/display_name from the identity system + the persona prose; profiles may point elsewhere |
| 4 | **environment** | generated per turn, never persisted | wd + project root (io §3a), OS/arch, date, resolved model + effort, active mode badges (planning/orchestrate), locations (memory root, skills roots) |

### 1a. Fragment role map (normative — the authority gradient)

Roles encode WHO is speaking: **system > developer > user = constitution > harness-generated state > content.**

| role | fragments | why |
|---|---|---|
| **system** | segments 1–4 above, only | the constitution; nothing else gets its authority |
| **developer** | skills catalog (agora-spec-skills §3.1), MEMORY.md index (agora-spec-memory §2) | harness-generated CATALOGS — inventory, not law; regenerated state kept out of the stable system segment |
| **user** | AGENTS.md/CLAUDE.md docs (subagents §6), invoked skill bodies (skills §3.3), working set / tool results (context-curation) | content, not authority — project prose at user-role makes the security asymmetry (§5) mechanical, not just stated; memory bodies arrive via tool reads, where models actually trust content (the wset rule). Memory is point-in-time and can be WRONG (operator: non-canon still surfacing in the canon) — instruction-weight recall turns errors into defended drift |

"Developer role" is agora's label, not every provider's: bridle translates per provider (native developer role where it exists; a post-core system block on Anthropic-shaped APIs) — a bridle normalization duty (agora-spec-bridle §3).

## 2. The core contract

Strictly **contract, deliberately small** — the prose face of machinery that is identical across profiles:

- tool discipline (families, when to search vs act, tool results are ground truth);
- approval semantics: a deny carries a message and the message is feedback — adapt, don't retry verbatim;
- the planning contract: what the plan artifact is, and **suggest entering planning when work looks big** (multi-file, multi-stage, ambiguous requirements, destructive/cross-cutting) — advisory, never automatic (agora-spec-planning-questions §3);
- the question contract, both registers (agora-spec-planning-questions §4): conversational for design/open questions — one at a time, logical order, state your leaning and reasoning, invite counter, converge on shared understanding, push back when you have better information; structured cards only for questions that outlive the conversation; never fabricate a missing answer;
- output conventions per surface (final-message-carries-everything for pipe/dispatch consumers);
- the security-asymmetry statement (§5).

**Rule that keeps the core small:** if a profile needs to *contradict* the core, the core has scope creep — fix the core, never fork prompts. Register/voice/posture differences are profile-block and persona content. Reference material (how-to, recipes) is skills, not prompt — models trust tool results over prose (the wset finding); the prompt states contracts, skills carry knowledge.

The core is a **tested artifact**: versioned in-repo, eval rows per model (cwbench-style) + a contract checklist (§6) gate changes.

## 2a. Overriding the built-in core (operator, 2026-07-16)

The built-in core is a default, not a cage — sovereignty means owning the constitution. Override is **user-layer-and-above only**:

- **A core is a PACKAGE, not a file** (operator, 2026-07-16) — so a variant carries its own model-specific material. Canonical layout (single file `cores/<name>.md` stays valid as the degenerate case):

  ```
  ~/.agora/prompt/cores/<name>/
    manifest.toml        # name, base_version (drift rail), notes
    core.md              # full contract — OR segments/<segment>.md (either, not both)
    segments/<segment>.md
    dialects.toml        # optional per-model knob OVERRIDES scoped to this core
                         # (defaults come from the bridle registry, §4)
    renditions/<model>@<core-hash>.md   # compiled renditions OF THIS CORE
  ```

  Renditions live with the core they render (they're keyed to its hash anyway); `agora prompt compile --core art --model ornith` writes there. The built-in core ships embedded in the same package format — one format everywhere; override = shadowing the package by name.
- **Full override**: `~/.agora/prompt/core.md` (or the package dir `~/.agora/prompt/core/`; system layer `/etc/agora/prompt/` beneath) replaces the built-in core.
- **Per-segment override**: `segments/<segment>.md` (segments = the §2 sections: tool-discipline, approvals, planning, questions, output, security) replaces one section, inherits the rest — survives binary upgrades better than a full fork.
- **Named variants**: packages under `~/.agora/prompt/cores/`; a profile selects one with `prompt_core = "<name>"`. Profiles *reference* variants; only user-layer-and-above may *define* them.
- **Never from the project layer or the dispatch envelope.** A cloned repo's `.agora/` gets AGENTS.md (additive context), never the constitution; provision material composing a brief already controls the task prompt — letting it swap a pod's core is privilege escalation. This line joins the config security-asymmetry list in the index.

**Rails that keep the escape hatch honest:**

1. **Drift protection**: override files carry `base_version:` frontmatter naming the built-in they forked from; on upgrade, built-in > base ⇒ a LOUD warning (status/doctor, not a log line) — the override still runs, you just can't not-know it's stale. `agora prompt show --effective` / `--diff` make the running core inspectable at all times.
2. **An override IS a new core version**: the pipeline resolves core = (built-in | override | variant) first, and everything downstream keys off the *effective* core's hash — per-model renditions regenerate against it, the §6 eval matrix runs against it. Overriding changes whose text is tested, not whether it's tested.

**Tooling (`agora prompt …`)** — the package has machine-checked invariants; the commands are what make the rails real:

```
agora prompt new <name> [--from built-in|<core>] [--segments planning,output] [--profile <p>]
                                  # scaffold a package by FORKING: copies the source text (full, or
                                  # named segments — rest inherit), stamps base_version from the
                                  # binary honestly; --profile wires prompt_core into that profile.
                                  # Edit a working constitution, never a blank page. Scriptable —
                                  # one line in a pod image build.
agora prompt show [--core <name>] [--effective --model <m>] [--diff]   # what actually runs (§2a rails)
agora prompt compile --core <name> --model <m>                         # write renditions/<model>@<hash>.md (§4)
agora prompt check                # manifest valid, segment names ∈ built-in's segment set,
                                  # drift status, stale renditions; also runs under `agora doctor`
agora prompt rebase [--core <name>]   # the drift warning's exit: show what the built-in changed
                                      # since base_version, operator folds in, re-stamps
```

**Specialized dispatch pods** fall out of this (operator): a workload-typed pod image (an art pod for maren-class work, a research pod) bakes its variant into the image's user layer — `~/.agora/prompt/cores/art.md` + `prompt_core = "art"` in the pod's profile. The pod is operator-provisioned infrastructure, so this is legitimate user-layer authorship, and the workload gets a constitution structured for it (art-direction discipline, asset conventions, look-first acceptance) instead of a dev core wearing a costume.

## 3. Render pipeline — deterministic, regenerated

```
compose(core_version, profile, identity, env, resolved_model) → prompt bytes
```

- **Pure function; byte-stable when inputs are stable.** Prefix caching lives on stable prompt bytes (the NET-46 / deepagents lesson; Ornith APC returns eventually) — never render differently on identical inputs. Environment fields that change per turn (date) sit in the environment segment *last-ish* so the stable prefix stays long.
- **Regenerated every turn from sources** — config/persona/skill-catalog edits take effect next turn with no restart (the funnel SystemPromptFn pattern, kept). The thread is never mutated; only this regenerated state changes (agora-spec-context contract #1/#3).
- **Dialect follows the RESOLVED model, not the session default.** With `plan_model`, escalation, or `%alias` one-shots the brain can change turn-to-turn; the dialect layer is applied at the point bridle resolves the model. A model swap invalidates any prefix cache anyway — re-render on swap is free.

## 4. Per-model dialects and renditions

Models have preferred prompting styles (operator, 2026-07-16). Two mechanisms, both keyed off the **bridle registry** entry (agora-spec-bridle — registry gaps become bridle tickets):

- **Dialect knobs (v1 default)** — structured, presentation-only: tool-call idiom (native tool_use vs qwen-XML habits), verbosity ceiling, format markers (markdown headers vs flat imperative lists for gemma-class), thinking guidance (Ornith: enable_thinking handling + phrasing that doesn't invite loops — the NET-36 lesson), optional per-segment *rewording* override for stubborn cases. **A dialect may rephrase, reformat, re-emphasize; it may never add or remove contract.**
- **Compiled renditions (opt-in per model)** — a full model-specific rendering of the core, generated at BUILD time (LLM-assisted rewrite into the model's preferred style is fine), hash-pinned, regenerated whenever the core changes, adopted only after that model's eval rows + the contract checklist pass. Compilation, never runtime improvisation — the mitigation for silent semantic drift is "it's a build artifact with a diff and an eval gate." Ornith is the expected first customer.

```toml
# bridle registry entry (illustrative)
[models.ornith.prompt]
dialect   = { tool_idiom = "qwen-xml", format = "flat", thinking = "off" }
```

Resolution per (core, model): registry knobs (model-global defaults) ← the core package's `dialects.toml` (per-core adjustments) ← the core package's `renditions/<model>@<hash>.md` if present and hash-current (replaces knob-transforms entirely). Knobs are mostly model properties and rarely vary by core; renditions are always per-core (§2a layout).

- **The claude-code lane is the most extreme dialect**, not a second system: render target `append` (vs `full`) — the overlay knows it is composing onto CC's built-in prompt via `--append-system-prompt`, so it drops restated tool mechanics and adds only the agora contract on top. One source tree, two render targets, same pipeline.

## 5. Security asymmetry (prompt edition)

Same rule as config layering (agora-spec index): lower layers may add context, never widen authority. **Project-layer prose (AGENTS.md/CLAUDE.md) is context, not authority** — it cannot loosen approval policy, identity, sandbox rules, or the question/planning contracts, and the core contract SAYS so, in the prompt, so the model has the rule even when the injected content argues otherwise. A cloned repo must never be able to talk the harness out of its envelope.

## 6. Eval

Matrix = **models × core version**. A core change triggers all model rows; a dialect/rendition change triggers only that model's rows. Rows = cwbench-style task passes + the **contract checklist**: under this prompt does the model still (a) treat deny-message as feedback, (b) suggest planning on big work, (c) question conversationally in design contexts and never fabricate missing inputs, (d) respect project-prose-is-not-authority. Suspected "this model wants different phrasing" becomes a measured dialect experiment, not a guess.
