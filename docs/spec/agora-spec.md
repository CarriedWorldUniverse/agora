# agora — the reusable sovereign agent harness (spec index)

2026-07-15. agora = **Go harness on funnel/bridle** — a *complete, reusable* harness packaging the ecosystem already built (funnel loop + ToolRunner, bridle model routing, nexus identity/dispatch, cairn, vessel, skills store) into one product instantiated however needed: a development agent (replacing codex CLI and Claude Code), a chat assistant, a computer-use agent, a broker-dispatched aspect. NOT a codex fork — codex (`~/external/codex/main`, Apache-2.0, @0.145.0-alpha.13) served as the spec-extraction reference; each spec below carries the extracted contracts plus agora decisions.

## Profiles — one harness, many instantiations (the point)

A **profile** is a named bundle turning the generic harness into a specific product. Everything spec'd below is profile-selected; nothing is hardwired to "coding":

```toml
[profile.dev]             # the codex/Claude-Code replacement
tools     = ["fs", "exec", "mcp", "agent", "workflow"]
skills    = [".agents/skills", "~/.agora/skills"]
modes     = ["orchestrate"]
frontends = ["tui", "daemon"]
approval  = "prompt"

[profile.chat]            # assistant: talk, browse, remember — no repo mutation
tools     = ["web", "mcp", "memory", "agent"]
frontends = ["daemon"]    # chat webpage + vessel attach
approval  = "auto-safe"

[profile.operator]        # computer use / driving desktop + web
tools     = ["browser", "computer", "mcp"]
approval  = "prompt"

[profile.aspect-<name>]   # broker-dispatched: identity + skill-scoped, headless
identity  = "nexus:<aspect>"
frontends = ["dispatch"]
```

A profile selects: **tool families** (fs/exec/web/browser/computer are pluggable ToolRunner families; `computer` = the computer-use family), skills roots, agent defs, modes, the model-alias table (or overrides), approval policy, hooks layers, which **surfaces** are enabled (the `frontends`/`daemon`/`dispatch` values are attach-surfaces + permitted client kinds — daemon listener, broker dispatch — not a rendering layer; per agora-spec-io, daemon = hub, TUI/web/vessel = attachable clients, dispatch = a controller), and identity (local user vs nexus aspect). Selected via `agora --profile chat`, per-daemon default, or the dispatch envelope. Computer use is not special: it's a tool family the `operator` profile enables — same loop, same approvals, same hooks, same workflows.

This is the nexus team-manager ideal made concrete: aspects become agora instances wearing profiles; the dev TUI, the chat webpage, and a dispatched builder are the same binary differently configured.

## Identity — first-class citizen (operator, 2026-07-15)

Every agora instance runs **as** an identity; config references it instead of being duplicated per instance.

```toml
[identity]
id           = "shadow"            # or set by profile / dispatch envelope
kind         = "aspect"            # operator | aspect | service | device (device = a controller key, remote spec)
display_name = "shadow"
credentials  = "keyring:nexus/shadow"   # keyfile/keyring ref for broker & MLE static key
```

- **Resolution precedence**: dispatch envelope > `--identity` flag > profile's `identity =` > daemon default > local OS user. Resolved once at session start, immutable for the session.
- **Interpolation**: any config string value may reference `{identity}` (= id) or `{identity.<field>}` (`id`, `kind`, `display_name`); substituted at config-load time. The motivating case — **one global comms MCP server instead of N custom configs**:

  ```toml
  [mcp_servers.comms]              # defined ONCE, globally
  command = "comms-mcp"
  args    = ["--identity", "{identity}"]
  env     = { COMMS_IDENTITY = "{identity}" }
  ```

  Every instance — shadow, plumb, anvil, the chat assistant — shares this stanza; each resolves to itself. Same interpolation works in hooks env/commands, model-alias tables, and skills roots.
- **Identity flows through everything already spec'd**: transcript/event attribution (items carry the acting identity), approval audit (`approval.resolved {by}`), broker auth (credentials ref = the aspect keyfile), remote-control MLE (the instance's static key belongs to its identity; *device* identities from agora-spec-remote are who controls, *instance* identity is who acts — distinct, both attributed), subagents (children inherit the instance identity; a future cross-identity spawn is a dispatch, not an agent()).
- Aspects = scope + accountable identity (the nexus ideal): profile supplies the scope, identity supplies the accountability, agora binds them.

### Identity bytes — provenance (generic users, no nexus required)

- **The keypair IS the identity.** Canonical identity bytes = the instance's static public key; the authoritative id is its fingerprint (`agora:<base32(sha256(pubkey))[..16]>`, short-form first 8 chars for display). `id = "shadow"` is a **petname/label** carried in config and metadata — never a trust anchor; two instances may claim the same name, they cannot claim the same fingerprint.
- **Auto-generated, self-sovereign.** First `agora init` (or lazily on first daemon start) generates the static keypair locally — OS keyring where available, else `~/.agora/identity/key` chmod 0600. No registration, no authority, no network: a fresh install has a working identity in zero steps, globally unique without coordination (same trust model as SSH host keys).
- **External identities BIND to the key, never replace it.** A nexus aspect keyfile, a herald/OIDC assertion, an org CA — all are *certifications attached to* the local pubkey (`credentials = "keyring:nexus/shadow"` links the binding). Peers that don't care about nexus verify the key alone; peers that do can demand the binding. This keeps agora usable by anyone while letting shadow's fleet run accountable aspects on the same mechanism.
- **Rotation & backup**: `agora identity export` (operator ceremony, encrypted bundle) / `import`; rotation = generate new key, sign it with the old one (continuity statement kept in the identity dir), re-enrollments cascade. Losing the key without a backup = a new identity; bindings and device enrollments re-establish — deliberate, that's what keyless recovery *should* cost.
- Per-profile identities are separate keypairs under the same store (`~/.agora/identity/<name>/`) — the chat assistant and the dev instance need not share bytes; whether they do is an operator choice, not a constraint.

### Identity sources (operator, 2026-07-15)

`identity.source` selects where the bytes come from; everything downstream (fingerprint id, interpolation, attribution, MLE) is source-agnostic:

```toml
[identity]
source = "local"                    # default: auto-gen as above
# source = "keyring:nexus/shadow"   # load an EXISTING keypair from the keyring
# source = "herald:shadow"          # identity issued/certified by herald
```

- **`local`** — the zero-step default (above).
- **`keyring:<ref>`** — use an existing keypair from the keyring (OS keyring or the custodian-vault-backed store; aspect keyfiles land here). agora does not generate; it loads. Fingerprint id derives from the loaded pubkey, so an aspect's agora instance IS the aspect, cryptographically — same key that authenticates to the broker.
- **`herald:<name>`** — identity from the herald IdP, two modes:
  - **certify (default)**: agora generates/holds the local key as usual; an enrollment ceremony against herald (operator passkey auth, per nexus-auth) has herald **sign the local pubkey** and return the identity metadata (canonical name, kind, display_name) as a binding. Private key never leaves the device; herald is the naming/certification authority, not the key custodian. Revocation = herald revokes the binding; the key survives as a bare local identity.
  - **provision (ephemeral)**: for short-lived instances (dispatch pods), herald issues a complete short-TTL keypair delivered through the dispatch envelope; no local persistence, expires with the pod. Flagged explicitly (`ephemeral = true`) — never the default, because delivered private keys are custody, not sovereignty.
- Sources compose with the precedence chain already defined (dispatch envelope > flag > profile > daemon default): a profile can pin `source`, and the dispatch envelope's provisioned identity outranks it.
- **Pluggable via standard (later add, operator 2026-07-15)**: the source interface is a Go `IdentityProvider` (resolve → keypair-or-cert + metadata; enroll ceremony; revocation check), and its semantics deliberately align with **W3C DIDs**: our fingerprint id is `did:key` in all but encoding (adopt the encoding when the plugin layer lands, keep `agora:<fp>` as the display alias); bindings/certifications are Verifiable-Credential-shaped (issuer signs subject-pubkey + claims); herald certify-mode = a VC issuer; a third-party identity system plugs in as a DID method/VC issuer implementing the same interface. v1 ships local/keyring/herald built-in only — no DID/VC stack dependency, just interface alignment so plugging in later is additive. (SPIFFE/SVID noted as the alternative standard for k8s workload identity — a SPIFFE provider would also fit the interface if dispatch pods ever want platform-issued identity.)

## Config layering (single statement — every subsystem defers here)

Layers, lowest → highest precedence: **built-in defaults < system (`/etc/agora/`) < user (`~/.agora/`) < project (`<root>/.agora/`) < profile overlay < env/flags < dispatch provision < runtime `config` messages** (admin-capability-gated). Per layer, both `config.toml` and standalone files (hooks.json, .mcp.json) load; a dot-dir is read once.

**Security asymmetry (the one rule that matters):** the project layer may *add* capability (skills, hooks — trust-gated, MCP servers — first-use-prompted) and may *restrict* anything, but may never *widen* security-relevant settings: identity, approval presets/policy, sandbox envelope, remote-control enablement, device enrollments, and the core-prompt override (agora-spec-prompt §2a) are user-layer-and-above only. A cloned repo must never be able to loosen the harness. (Hooks' content-hash trust and MCP first-use prompts are instances of this rule.)

## The spec set (off-git, this directory)

| file | covers | key decision |
|---|---|---|
| `agora-spec-skills.md` | SKILL.md format, discovery roots, catalog injection + token budget, $mention, implicit detection | progressive disclosure exactly as codex/CC; `.agents/skills` = primary store (matches nexus convention) |
| `agora-spec-hooks.md` | 10 lifecycle events, hooks.json schema, per-event stdin/stdout contracts, exit-code semantics, trust model | adopt codex's superset (incl. PermissionRequest — the funnel escalation seam); implement `async:true` (both parents don't) |
| `agora-spec-mcp.md` | the **tool-layer** spec (title lags scope): mcp_servers config, manager behaviors (`mcp__server__tool` naming, catalog cache), **wasm module transport** (§1a — in-process wazero, hash-pinned, grant-sandboxed), OAuth, elicitation (surfaced as the `question` kind), **deferred tools + tool_search** (registry-wide), and **native tool families + the fs-watcher runtime** (§5a — staleness signal for the edit-guard and context curation) | wire protocol = official Go MCP SDK; agora supplies config + policy + native-family runtime; reads Claude `.mcp.json`; wasm = the sovereignty transport (v1.1–v2) |
| `agora-spec-subagents.md` | agent defs (.md + frontmatter), agent tool semantics, graph persistence, **orchestration mode** (planner delegates all execution to subagents/workflows), AGENTS.md discovery/merge | **Claude Code model as base** (operator decision); codex v1/v2 documented for import |
| `agora-spec-workflows.md` | starlark workflow scripts: ctx.agent/parallel/pipeline, journal + resume, pattern library | **starlark** (operator decision); agora's differentiator — neither parent ships this |
| `agora-spec-tui.md` | inline viewport + scrollback history, two-region streaming, approval modal, composer, minimum-lovable cut | lean Go TUI (bubbletea), ~15 files of design, not 366 |
| `agora-spec-io.md` | **agora as daemon/driver**: multi-attach clients (TUI + web page + vessel on one thread), fan-out events, first-answer-wins approvals, presence; plus pipe mode + Go library | **chainability with agora as the hub** (operator): daemon owns sessions, frontends are interchangeable concurrent clients; freeze this seam first |
| `agora-spec-remote.md` | **remote control = any controlling connection**: MLE (Noise IK, PQ-hybrid target) keyed off passkey-style device keys, external via the **interchange** (dumb relay), pairing-code enrollment w/ operator ceremony, capability scopes (observer/interactive/approver/admin), device list/revoke | operator: MLE always (transport never trusted); codex System B crypto + System A pairing UX + the scope layer codex omitted |
| `agora-spec-approvals.md` | canonical approval model: (kind, decision, scope), per-kind policy, named presets, exhaustive alias mappings (profiles/pipe flags/MCP modes/hook permission_mode/TUI options), invariants | one model, everything else a declared alias; timeout always deny; subagents inherit, workflows may only restrict |
| `agora-spec-bridle.md` | what agora REQUIRES of bridle: registry (aliases, context_window, capabilities, pricing), normalized stream events, tool/structured-output/effort/error normalization, cache hints; gap checklist | bridle = the multi-model layer (operator); gaps become bridle tickets, not agora workarounds |
| `agora-spec-context.md` | context-management SEAM: ContextManager interface + 8 fixed contracts (thread never mutated, hooks fire, state regenerated not summarized, wire events, compact-and-retry) | stable seam; the algorithm plugs in behind it (below) |
| `agora-spec-context-curation.md` | the ALGORITHM behind the seam: working-set ledger (one live copy per keyed artifact), two-layer resident/tracked budget w/ hot-set immunity + hysteresis, staleness invalidation, partial re-admission via SpanIndexer, summarization as last resort | settled 2026-07-15 from the ctxmap/wset testing (curation beats memory stores, 12/12 vs 4/12, 4–20× fewer tokens); rule: models trust only tool results, so curate where they look |
| `agora-spec-persistence.md` | thread persistence: JSONL source-of-truth (meta line + append-only items, fork-by-reference), SQLite mirror (rebuildable; wd-indexed, agent edges, FTS), Go ThreadStore interface, retention | codex three-layer shape adopted (operator accepted rec); compaction never rewrites; index corruption ≠ data loss |
| `agora-spec-memory.md` | v1 file-based identity-scoped memory: MEMORY.md index + one-fact-per-file (shadow's proven format, dirs usable as-is), budgeted index injection, `memory.*` tool family w/ atomic index updates | minimal by decision; codex memories pipeline stays parked; smarter recall slots behind the same surface later |
| `agora-spec-prompt.md` | system-prompt composition: segment order (core contract → profile → identity/persona → environment), deterministic per-turn regeneration, per-model dialect knobs + compiled renditions, full-vs-append render targets, prompt security asymmetry | **one shared contract core for all profiles** (operator 2026-07-16); dialects are presentation-only; renditions = eval-gated build artifacts, never runtime improv; core overridable at user-layer+ (§2a — full/segment/named variants w/ drift rails) → specialized workload pods (art pod etc.) bake their own core |
| `agora-spec-planning-questions.md` | planning = design→spec→plan phases; plan artifact + `plan` tool; the planning posture (one posture, two exits: inline / orchestrate-delegate); `plan` gate + `question` kind on the approvals pipeline; conversational vs structured registers; escalation ladder (park / die-honestly-and-redispatch / bubble) | planning gate **opt-in with suggest-when-big** (operator 2026-07-16); questions are missing information — **never fabricated, never timeout-denied**; builders go `blocked: needs-input` instead of asking |
| `agora-spec-build.md` | the one-shot build decomposition: U0 contracts-as-code → skeleton → three waves of bounded units w/ dependency edges + observable DoD each; hybrid fabric (pool for bounded units, shadow for seams/reviews); U18 conformance suite grows continuously | the spec set IS the build brief (operator 2026-07-16); contracts compile before fan-out; interactive exceptions = core prompt text, TUI feel, curation tuning; old TUI freezes on `v0-legacy` |

## Architecture (one paragraph)

funnel remains the agent loop + ToolRunner (3 lanes) speaking to models via bridle; agora adds (1) the **capability layer** — skills catalog/injection, hooks engine, MCP manager, AGENTS.md context, subagent spawner, workflow engine — as packages hanging off funnel's turn assembly and tool registry, and (2) the **TUI**, a thin client on the funnel session seam. Codex's lesson worth keeping even though we're not using its code: put a hard protocol seam between core and every frontend (TUI, headless exec with JSONL events, web later, nexus dispatch later) — codex's app-server proves one core can serve them all.

## Format-compatibility matrix (the migration story)

agora reads, unchanged: Claude Code `SKILL.md` (+ `.claude/skills/`), `.claude/agents/*.md`, `.mcp.json`, `CLAUDE.md` (as AGENTS.md fallback filename), Claude `hooks.json`; codex `AGENTS.md`, `agents/openai.yaml` sidecars (recognize skills-as-agents on import), codex hooks.json (same shape). Claude slash commands import as skills (codex's `source-command` prefix trick).

## Suggested build order

0. **I/O contract + pipe mode** (agora-spec-io.md) — the seam everything else consumes; freeze it first. Remote-control MLE (agora-spec-remote.md) layers on it: UDS exempt in dev, tailnet MLE early, interchange path v1.1.
1. **Skills + AGENTS.md + prompt composition** (agora-spec-prompt.md — pure read/inject/compose, no new runtime machinery; instant value in funnel; dialect knobs ride the bridle registry).
2. **MCP manager** (Go SDK + config/policy layer) — unlocks the tool ecosystem. (The wasm transport, mcp §1a, lands v1.1–v2 after the stdio/http paths; first module = the identity-aware comms server.)
3. **Subagents** (child funnel sessions + agent defs + graph store).
4. **Hooks** (touches approval path; wants stable tool events first).
   4a. **Planning + questions** (agora-spec-planning-questions): the `plan` tool/item is cheap and can land any time after 0; the `plan`/`question` approval kinds land with 4's approval path; parked threads ride 0's io seam.
5. **Workflows** (starlark on top of 3; `ctx.question`/`ctx.approval` require 4a).
6. **TUI** (can start any time against the session seam; approval modal needs 4's PermissionRequest for the full loop).

## Also extracted, parked (not in scope, documented in session transcripts)

codex sandbox stack (bubblewrap profile, execpolicy starlark DSL — natural later port, shares starlark), network-proxy egress design, apply-patch fuzzy format, memories pipeline, app-server protocol details.
