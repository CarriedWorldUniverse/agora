# agora spec — MCP client

Extracted 2026-07-15 from codex-rs (`config/src/mcp_types.rs`, `codex-mcp/`, `rmcp-client/`). Go note: the official `modelcontextprotocol/go-sdk` owns the wire protocol; this spec is the **config surface + manager policy** layered on top. Compat targets: agora reads both its own TOML and Claude Code's `.mcp.json` (`{ "mcpServers": { ... } }` or bare map).

**Scope note:** this file is the tool-layer spec, not just MCP. §5 (deferred tools / tool_search) governs the whole registry — native families AND MCP — and §5a specs the native fs/exec tool families and the fs-watcher runtime they share. (Title kept for stable cross-refs; treat as "tools + MCP".)

## 1. Per-server config schema

Keyed map: `[mcp_servers.<name>]` (TOML) / `"mcpServers": { "<name>": {...} }` (JSON). Transport chosen by presence of exactly one of `command` (stdio), `url` (streamable_http), or `module` (wasm, §1a); mixing transport fields = hard error.

**Identity interpolation (agora extension, not in codex/CC):** any string value (`args`, `env`, `url`, `http_headers`, `cwd`) may reference `{identity}` / `{identity.<field>}`, substituted at config-load from the instance identity (index spec, "Identity" section). This is what allows ONE global comms MCP stanza shared by every instance instead of per-instance configs. Interpolated values participate in the catalog-cache fingerprint (§2) after substitution, so two identities sharing a stanza correctly get distinct cache entries.

**stdio:** `command` (required), `args` (default []), `env` (literal map), `env_vars` (names to *forward* from host env; entries are `"NAME"` or `{name, source: local|remote}`), `cwd`.

**streamable_http:** `url` (required), `bearer_token_env_var` (env var name → `Authorization: Bearer`), `http_headers` (literal), `env_http_headers` (header → env-var-name). Never accept a raw `bearer_token` literal in config.

**wasm:** `module` (required; file path or `embedded:<name>`), `module_hash` (required), `grants` sub-table, `env` (literal map — the only env the module can see). Full transport spec in §1a.

**Shared (all defaults noted):**
| field | default | semantics |
|---|---|---|
| `enabled` | true | false ⇒ skipped entirely |
| `required` | false | headless/exec errors if a required server fails init |
| `startup_timeout_sec` | 30s | also accepts `startup_timeout_ms`; sec wins if both |
| `tool_timeout_sec` | 300s | per tool call (and resource ops) |
| `supports_parallel_tool_calls` | false | advertise tools parallel-safe |
| `enabled_tools` | — | allow-list; if set, only these registered |
| `disabled_tools` | — | deny-list, applied after allow-list |
| `tools.<name>.approval_mode` | — | per-tool override |
| `default_tools_approval_mode` | auto | `auto \| prompt \| writes \| approve` |
| `auth` | oauth | `oauth \| chatgpt` (agora: drop chatgpt; keep slot for herald later) |
| `scopes`, `oauth.client_id`, `oauth_resource` | — | OAuth extras (RFC 8707 resource) |
| `environment_id` | "local" | multi-environment routing (agora: keep, maps to executor envs later) |

**Global config:** `mcp_oauth_credentials_store` = `auto|file|keyring` (auto: prefer keyring, fall back to file); `mcp_oauth_callback_port` / `mcp_oauth_callback_url` (default 127.0.0.1 loopback).

Shared fields apply to wasm servers unchanged (`enabled`, `required`, tool filters, approval modes, `tool_timeout_sec`) except `auth`/`scopes`/`oauth.*`, which do not exist for wasm — there is no HTTP identity; a module's outbound auth is a host-function concern (§1a).

Filter rule: tool allowed iff `(enabled_tools is unset || contains) && !disabled_tools.contains`.

## 1a. WASM module transport (operator, 2026-07-16)

Third transport: an MCP-shaped server compiled to **wasm32-wasi**, executed **in-process** via **wazero** (pure Go, zero cgo — embeds like go.starlark.net, identical on amd64/arm64). Why it exists — the sovereignty transport: stdio in practice means `npx`/`uvx` resolving a dependency tree at session start with full user authority; a wasm module is a single content-addressable artifact with **zero ambient authority** — it cannot touch fs/network/env unless the host grants it, so "this tool can only do X" is enforced by the instruction set, not by policy. Timing: v1.1–v2 addition; stdio/http remain the ecosystem-compat transports and are built first.

### Config

```toml
[mcp_servers.comms]
module      = "~/.agora/wasm/comms.wasm"   # or "embedded:<name>" (compiled into the agora binary)
module_hash = "sha256:9f2a…"               # REQUIRED; mismatch = refuse to load, hard error, never a warning
env         = { IDENTITY = "{identity}" }   # identity interpolation per §1 works unchanged

[mcp_servers.comms.grants]                  # deny-by-default; table absent = pure-compute module
fs_read  = ["{working_dir}"]                # preopened dirs only; path-checked at the host boundary, symlinks never escape (same rule as the fs-watcher, §5a)
fs_write = []
net      = ["https://herald.internal/*"]    # origin allow-list; the HOST does the dial — module gets an http_fetch host function, never a socket
env      = ["IDENTITY"]                     # only named vars visible
clock    = true                             # wall clock is a grant too (default false)
```

- **`module_hash` is mandatory** — pinnable code is the point of the transport (stdio can't pin what npx resolves). Updating a tool = updating the hash: deliberate, diffable, auditable. Same content-hash trust model as hooks: first-use prompt, re-approve on hash change; the config-layer security asymmetry (index) applies — a project-layer wasm server is first-use-prompted like any MCP server.
- **Grants are the manifest; escalation is the exception path.** A runtime import attempt outside the grants raises an `escalation`-kind approval (approvals §1) instead of crashing; an allow may scope `once`/`session` and journals like any decision. (The transport may ship hard-deny first; the escalation path can lag one release.)
- **All I/O is host-mediated**: grants resolve to Go host functions (`http_fetch`, preopened WASI dirs, named env vars), centrally logged/rate-limitable; the module never holds a raw capability. Writes landing inside the wd are ordinary disk writes — the fs-watcher (§5a) sees them like anyone else's.

### ABI (deliberately boring)

Flat JSON over exported functions, **WASI preview 1** — NOT the component model / preview 2 (still churning, wazero support partial; migrating off the flat ABI later is trivial, coupling to an unstable standard now is not). Exports:

- `list_tools() → json` — the same tool-descriptor array a wire MCP server returns (name, description, input schema); called once at load, feeds the same catalog.
- `call_tool(name, args_json) → result_json` — one synchronous entry point; the host enforces `tool_timeout_sec` via context-cancelled invocation on a goroutine.
- Memory passing: standard `alloc`/`free` exports; host writes the request into linear memory, reads the response back.

Any language targeting wasm32-wasi can author modules (Rust is smoothest today; TinyGo works with stdlib gaps). Elicitation (§4) is N/A — the flat ABI has no server-initiated requests; revisit with the component model.

### Lifecycle & composition

- **Startup** = load-or-AOT-compile (compiled artifact cached, keyed by `module_hash`) + instantiate + `list_tools`. Participates in §2's `Starting → Ready/Failed` events and `required` gating — just fast (µs instantiation on cache hit; no process spawn, no stdio framing, nothing for the manager to babysit).
- **Catalog cache (§2)**: wasm catalogs cache with an *exact* key — `module_hash` + interpolated env — no stdio fingerprint heuristics needed; valid indefinitely per hash.
- Everything downstream is unchanged by construction: `mcp__<server>__<tool>` naming, deferral/tool_search (§5), per-tool approval modes, `tool.loaded` events.
- **First consumer**: the identity-aware comms server (index, Identity §) ships as the first wasm-native module — the tool most worth having on the owned, hash-pinned path, and it exercises interpolation + net grants + the trust model end-to-end.

### Non-goals

Not the workflow runtime (starlark stays — conversationally-editable source is the construction UX, workflows spec §5); not a sandbox for `exec` (the model's shell is io §3a's business — wasm sandboxes tool *implementations*, not the model's commands); not a migration target for the Node/Python MCP ecosystem (stdio/http remain the compat transports).

## 2. Manager behaviors (port these)

- **Eager concurrent startup**: one goroutine per enabled server at session start, emitting `Starting → Ready/Failed/Cancelled` events + a final aggregate `{ready, failed, cancelled}` for the UI. Per-server cancellation token. The client handle is a cached shared connect future — awaiting it never re-connects.
- **Required-server gating**: after startup, await all `required` servers and aggregate failures into one error (after the elicitation route is reachable — init can require elicitation).
- **Tool catalog cache** (fast session start): process-scoped LRU (cap 32, TTL 30 min). **Only stdio and wasm catalogs are cacheable** (wasm key is exact — `module_hash` + interpolated env, §1a); stdio key = server name + SHA1 fingerprint over (command, args, env, env_vars + their current values, cwd, environment_id, elicitation capability). Generation tickets: a publish is accepted only if its fetch generation > last accepted (slow fetch can't clobber a newer one). Serve cached tools before startup completes; refresh from live connection.
- **Tool naming**: model-visible name = `mcp__<server>__<tool>` (prefix `mcp__`, delimiter `__`) — same convention as Claude Code, keep it. Max name length 64: on collision or overflow, truncate + append 12-hex SHA1 suffix; deterministic ordering by raw identity. Keep raw server/tool names separately for protocol calls.
- **Per-call timeout** = server's `tool_timeout_sec`; reject calls to filtered-out tools.
- **Startup error UX**: special-case auth-required ("run `agora mcp login <server>`") and timeout ("bump startup_timeout_sec").

## 3. OAuth

- **Storage**: keyring (service name e.g. "Agora MCP Credentials") or fallback JSON file `~/.agora/.credentials.json` chmod 0600. Store key = `"<server>|<sha256(url-payload)[..16]>"`. Wrap every read-modify-write in a file lock (concurrent agora processes). Stored shape: `{server_name, url, client_id, token_response, expires_at_ms}`.
- **Refresh**: skew 30s (`now + 30s >= expires_at` ⇒ refresh). Near-expired tokens usable only if a refresh token exists. On load, reconstruct `expires_in` from `expires_at`; known-expired ⇒ zero so the SDK refreshes before first request. Persist refreshed credentials only when changed; delete when SDK reports none.
- **Auth status resolution** (order): bearer_token_env_var set → BearerToken; Authorization header present → BearerToken; stored OAuth usable → OAuth; stored but unrefreshable → LoggedOut(reauth); none → discover OAuth metadata (RFC 9728 protected-resource metadata via WWW-Authenticate, 5s timeout) → LoggedOut(login) or Unsupported.
- **Login flow**: local loopback callback server on `127.0.0.1:0` (or configured), PKCE, per-server callback id = 12 chars of base64url(sha256(server_url)), overall timeout 300s, `resource` query param when configured. Variants: interactive (open browser + print URL) and return-URL (hand the auth URL to a frontend and await completion) — the second is what the TUI/web UI wants.

## 4. Elicitation

Server-initiated elicitation surfaces as the **`question` kind with `source: mcp_server`** (approvals §1, agora-spec-planning-questions §4) — same card rendering, ladder, and never-fabricate rules as any question; policy per the presets' question column (an auto-deny toggle = declining with message). Per-request routing keyed by (server, request id), resolved by the answering client. The Go SDK surfaces the protocol; agora supplies this policy layer.

## 5. Deferred tools + tool search (operator-requested, 2026-07-15)

Don't load every tool schema at session start — the skills catalog's progressive-disclosure principle applied to tools. Applies registry-wide (MCP tools AND native tool families), per profile.

- **Registry states**: a tool is `core` (full JSON schema injected at start) or `deferred` (name + one-line description only, listed compactly in a system fragment: "deferred tools available via tool_search: …"). Per-profile config decides; sensible defaults: native families core; each MCP server's tools deferred once total tool count exceeds a threshold (e.g. >20 schemas or >N estimated tokens), or explicitly `defer = true` per server.
- **`tool_search` tool** (always core when any tool is deferred):
  - `query: "select:name1,name2"` — exact fetch by name;
  - free-text keyword query — ranked match over name + description (the weighted-lexical scorer extracted from codex's skills shadow experiment is the right ranking algorithm: name-match ≫ description-match, prefix-related bonus);
  - `max_results` (default 5).
  Returns the full JSONSchema definitions; from that point the tools are callable exactly like core tools. **Session-sticky**: once loaded, a schema stays loaded (and survives compaction as part of the tool state, not the transcript).
- **Interplay with the MCP catalog cache (§2)**: search runs over the aggregated catalog (live + cached), so deferred servers' tools are findable without their schemas ever having been injected. Eager startup still connects and lists; deferral is about *context injection*, not connection.
- **Events**: `tool.loaded {names}` on the I/O channel so frontends can show what's in scope; `/status` lists core vs loaded vs deferred counts.
- **Prompt guidance** (system fragment, one paragraph): batch needed tools into ONE tool_search call; prefer select: when names are known. (Both parents document this failure mode — per-tool round-trips.)
- Codex note for the record: codex *removed* its ToolSearch feature in favor of code-mode (tools-as-code in a V8 isolate with generated TS types). agora chooses tool search — no embedded JS runtime, fits the Go/starlark stack; code-mode stays parked as a possible later addition.

## 5a. Native tool families & the fs-watcher (runtime)

The native tool families (`fs`, `exec`, `web`, `browser`, `computer`) are pluggable ToolRunner families (index → Profiles); a profile enables a subset. They register into the same tool registry as MCP tools (§5), so deferral/tool_search and `mcp__`-style naming (native families keep their bare names, no prefix) apply uniformly. Distinct from families: the **harness-intrinsic core tools** — `plan` + `question` (agora-spec-planning-questions §1/§4), `tool_search` (§5), the `memory.*` family (agora-spec-memory §3) — engine-registered, always core, present in every profile (`question` is ladder-resolved per context rather than profile-gated). This section specs the one shared runtime component the fs/exec families need: the **fs-watcher**.

### The fs-watcher

A per-daemon service that tracks which files change under the session's writable roots, so the harness knows when a previously-read file is stale. It is a harness component, never a tool the model calls.

- **Scope**: watches the session **working dir + declared `add_dir` roots** (io §3a) — the sandbox *write* envelope. Ignores internal churn in protected dirs (`.git`, `.agora`, `.cairn` — io §3a): those change constantly and are never model-read artifacts.
- **Signal, not content**: emits `{path, kind: modified|created|deleted, at}` keyed by path — the same `(file, path)` key the context-curation ledger uses (agora-spec-context-curation §2). Change identity is the file's **content hash**: a write that produces identical bytes is a no-op, not an invalidation (avoids self-inflicted staleness when a tool rewrites a file unchanged).
- **Coalesced**: debounced per path (a burst of writes from one `run_command` is one event); the watcher reports the net state at quiescence, not every inotify tick.

### Two consumers (why it exists)

1. **Edit-tool staleness guard** (fs family): a dedicated `edit`/`apply_patch` tool rejects (or warns + requires re-read) when the target changed since the model last read it — the "don't edit a file that moved under you" guarantee a bash tool cannot give (this is the promote-to-dedicated-tool rationale). The check: the watcher's current on-disk hash for the key vs the **content hash the curation ledger stores for that key's live copy** (agora-spec-context-curation §2) — a mismatch means the file moved since the read. (The hash is a ledger field on resident keys, exactly the hot keys under active edit.)
2. **Context-curation staleness gate** (agora-spec-context-curation §2, §3b): a mutation the watcher attributes to a keyed file — especially a `run_command` that touched it without carrying full content — **invalidates** that key's live copy (stubbed `[modified since this read: re-read]`) rather than retaining stale bytes as truth. This is a correctness rule, not tuning.

### Implementation & fallback

- **Primary**: an OS file-watcher (`fsnotify`/inotify/FSEvents) rooted at the writable scope; codex's `file-watcher` crate is the extracted reference.
- **Fallback** (no watcher, or a working dir too large to watch): a periodic **mtime-sweep** of the writable roots (codemap's `SweepChanged` pattern) run between turns. It **over-invalidates** (a touched-but-unchanged file may re-stale) — the safe direction: worst case is an unnecessary re-read, never stale-content-as-truth. Config `context.fs_watch = "notify" | "sweep" | "off"` (default `notify`, auto-degrade to `sweep`); `off` makes the edit-guard read-before-write on every edit and the curation gate assume any non-content mutation invalidates.
- **Bound**: never follows symlinks out of the sandbox root; deleted-file events mark the key deleted (curation drops it; edit-guard errors on a stale edit).

## 6. agora-as-MCP-server (later)

Codex also exposes itself as an MCP server (single `codex` tool + approval elicitations). Cheap parity win once the core loop exists: an `agora` MCP tool wrapping a headless thread/turn, so other agents (including Claude Code) can drive agora.
