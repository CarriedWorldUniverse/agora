# agora spec — skills

Extracted 2026-07-15 from openai/codex @ ~0.145.0-alpha.13 (`~/external/codex/main/codex-rs`, crates `core-skills/`, `skills/`, `ext/skills/`). Compatibility targets: Claude Code SKILL.md files load unchanged; codex `agents/openai.yaml` sidecars are readable.

## agora build notes

- **Big alignment:** codex discovers skills in `$HOME/.agents/skills` and in repo `.agents/skills` dirs between project root and cwd — the *same convention as shadow's canonical skill store* (nexus repo `.agents/skills`). agora adopts `.agents/skills` as the primary store, `~/.agora/skills` as the harness-local one.
- **Progressive disclosure is the whole design**: catalog (name + description + path only) injected every turn under a token budget; full SKILL.md body injected only on invocation, non-sticky (must re-mention next turn). Port this exactly — it is what keeps skills cheap.
- Adopt the `$mention` sigil for explicit invocation (matches codex; Claude Code uses the Skill tool — agora's TUI/composer uses `$`).
- Skip for v1: plugin namespacing, product gating (unenforced even in codex), the shadow lexical-selection experiment, orchestrator `skills.list/read` tools. Keep the sidecar `agents/openai.yaml` *reader* for compat but agora-native metadata can live in frontmatter.

## 1. Format

A skill = a directory containing `SKILL.md`; optional sidecar `agents/openai.yaml`; conventional subdirs `scripts/`, `references/`, `assets/`.

### 1.1 SKILL.md frontmatter
Delimited by exact `---` lines (first non-empty content must be the opener). YAML. Only three keys read:

| key | required | rules |
|---|---|---|
| `name` | no — falls back to parent dir name | single-line-sanitized, ≤64 chars (qualified w/ namespace ≤128) |
| `description` | **yes** (non-empty or skill errors) | single-line-sanitized; ≤1024 effective at render |
| `metadata.short-description` | no | nested under `metadata:` |

Unknown frontmatter keys ignored (⇒ Claude Code SKILL.md with `allowed-tools` etc. loads fine, extras dropped — agora may choose to honor `allowed-tools`).
Whitespace runs collapse to single spaces. **Lenient-YAML repair** worth porting: if strict YAML fails, quote unquoted scalar values containing `": "` or leading `[ { @ \`` (e.g. `description: Build for AWS: ECS`), then retry.

### 1.2 Sidecar agents/openai.yaml (all blocks optional; parse failure = empty, never blocks the skill)

```yaml
interface:
  display_name: ""        # ≤64 or dropped
  short_description: ""   # ≤1024 (UI blurb; generator enforces 25–64)
  icon_small: ./assets/x.svg   # relative, must start assets/
  icon_large: ./assets/x.png
  brand_color: "#RRGGBB"  # exactly 7 chars hex
  default_prompt: ""      # ≤1024, should mention $skill-name
dependencies:
  tools:
    - type: mcp           # required per tool (only 'mcp' documented)
      value: serverName   # required
      description: ""
      transport: streamable_http
      command: ""
      url: ""
policy:
  allow_implicit_invocation: true   # default true; false ⇒ hidden from catalog, still $-invocable
  products: [codex]       # parsed, NOT enforced (skip in agora v1)
```

Skills double as **agent definitions**: an agent-only skill sets `policy.allow_implicit_invocation: false` and uses SKILL.md body as the agent's instructions (codex's review-agent sample). agora instead uses Claude-style agent defs (see agora-spec-subagents) but should recognize this shape on import.

## 2. Discovery

Roots (agora mapping), deduped by canonicalized path, highest precedence first:
1. Project: `<project>/.agora/skills` (scope Repo)
2. Repo `.agents/skills` dirs at every level from detected project root down to cwd (scope Repo). Project root found by scanning cwd ancestors for configurable root markers.
3. User: `~/.agora/skills` (scope User), `~/.agents/skills` (scope User); Claude Code compat (read-only import): `<project>/.claude/skills` and `~/.claude/skills` — matches the `.claude/agents` compat in agora-spec-subagents §1
4. System/bundled: `~/.agora/skills/.system` (scope System) — embedded skills unpacked at startup with a content-fingerprint marker file; matching marker ⇒ skip reinstall; mismatch ⇒ wipe+rewrite.
5. Admin: `/etc/agora/skills` (scope Admin) — optional.

Traversal guards: max depth 6 from root; ≤2000 dirs and ≤20000 entries per root; ≤64 concurrent loads. Hidden dirs skipped *below* the root (the root itself may be hidden — that's how `.system`/`.agents` work). **Symlinks (trust-scoped, NEX-750):** System never follows a symlink. User/Admin roots (the owner's own machine — trusted) follow symlinks anywhere, which is also what lets a dotfile-managed store or a cross-root canonical-dedup alias work. Repo roots (a potentially untrusted clone) follow symlinks but *contained*: a followed symlink — whether the `SKILL.md`/sidecar file itself or any parent directory in the walk — must resolve **within the project root**, so an untrusted clone cannot symlink out of the project to read an arbitrary host file, while a within-project (monorepo) shared-skills symlink (`.agents/skills/shared → ../../shared-skills`) still works. The same project-root containment applies to AGENTS.md ancestor-chain collection (§6). Missing root = empty, no error; per-skill parse errors surfaced as warnings (System scope errors silently dropped).

Prompt-ordering rank: System < Admin < Repo < User, then name, then path.

## 3. Context injection (the token model)

### 3.1 Catalog — every turn
A developer-role fragment wrapped in `<skills_instructions>…</skills_instructions>`:

```
## Skills
<intro line>
### Skill roots            (only when path-aliasing engaged)
- `r0` = `/abs/root`
### Available skills
- <name>: <description> (file: <path>)
```

Only skills with `allow_implicit_invocation != false` and enabled are listed. Optionally append a `### How to use skills` playbook section (trigger rules / progressive disclosure / context hygiene) when the model needs it.

### 3.2 Budget
- Default = **2% of context window in tokens** (min 1); no window known ⇒ 8000-char fallback. Token estimate ≈ bytes/4.
- Per-description cap 1024 chars (truncate with `...`).
- Fitting: (a) all full lines fit → done; (b) else minimum lines (`- name: (file: path)`) + round-robin description chars one at a time; (c) else minimum lines in scope-priority order until exhausted, rest omitted. Emit truncation/omission warnings.
- Alias optimization: if long absolute paths cause omission, switch to a `### Skill roots` alias table + relative paths when that fits more skills.

### 3.3 Invocation — full body, per turn, non-sticky
On explicit mention: read full SKILL.md (cap 8000 bytes, warn on truncate), inject as a user-role fragment:

```
<skill>
<name>{name}</name>
<path>{path}</path>
{contents}
</skill>
```

Name cap 256 bytes, path cap 1024. Not carried across turns unless re-mentioned.

## 4. Mention syntax & resolution

- Sigil `$`; name chars `[A-Za-z0-9_\-:]` (`:` for namespaced names).
- Linked form `[$name](path)` with path kinds `skill://`, `plugin://`, `mcp://`, `app://`, or anything ending `SKILL.md`.
- Env-var guard: ignore `$PATH $HOME $USER $SHELL $PWD $TMPDIR $TEMP $TMP $LANG $TERM $XDG_CONFIG_HOME` (case-insensitive).
- Resolution: exact path match first; then plain name only if globally unambiguous (name count == 1, no connector-slug collision). Ambiguous ⇒ ignored.
- Disabled skills = path present in a disabled-paths set (user toggle state).

## 5. Implicit invocation detection (heuristic, not model-based)

Worth porting — it's how the harness *notices* skill use for telemetry/UX without any model scoring:
- **Script run**: argv[0] ∈ {python python3 bash zsh sh node deno ruby perl pwsh}, first non-flag arg ends in `.py .sh .js .ts .rb .pl .ps1` → resolve path, walk ancestors, match a skill's `scripts/` dir.
- **Doc read**: a Read of a known SKILL.md path → that skill.

There is NO production model/embedding pre-filter in codex — the catalog is always fully injected within budget and the model decides. (A lexical scorer exists only as a measurement shadow experiment; skip.)

## 6. Claude Code compat deltas

- Same core format + progressive disclosure. Claude extras in frontmatter (`allowed-tools`, `license`, …) are ignored by codex; agora should at minimum ignore-without-error, ideally honor `allowed-tools`.
- Codex-specific: sidecar openai.yaml, `$` sigil, `metadata.short-description` nesting, scope system, length limits, frontmatter repair, `.system` install mechanic.
- Claude slash commands map to skills on import (codex prefixes them `source-command`); agora: import Claude `commands/*.md` as skills the same way.
