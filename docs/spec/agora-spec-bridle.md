# agora spec — bridle provider contract

Closes coherence hole #2 (2026-07-15). bridle is the existing Go multi-model layer (operator: "bridle is our multi-model implementation — use it"). This spec is **what agora requires of bridle** — the interface commitments other specs already lean on. Anything bridle lacks today becomes a bridle work item, not an agora workaround.

## 1. Registry & model info

- `Resolve(alias|id, identity) → ModelHandle` — aliases from agora config (with `{identity}` interpolation applied before resolution); unresolvable = error at session/run start, never mid-turn (workflows §2a).
- `List() → []ModelInfo` — feeds the TUI `%`-picker and `/model`.
- `ModelInfo` (required fields): `id`, `aliases`, **`context_window`** (tokens — the skills 2% catalog budget and context-manager depend on it), `max_output_tokens`, capabilities: `{tools, parallel_tools, streaming, reasoning_effort, structured_output, prompt_caching, vision, system_prompt_mode: full|append}` (`append` = lanes that only own an append slot, e.g. claude-code CLI — the compose branches structurally on it, agora-spec-prompt §4), optional `pricing {in, out, cached}` (enables cost-aware workflow budgets — token-only until present), optional `prompt {dialect | rendition_ref}` (MODEL-GLOBAL presentation knobs — tool idiom, format, thinking guidance; per-core adjustments/renditions live in the core package, not here — agora-spec-prompt §2a/§4).

## 2. Turn execution

`Stream(ctx, ModelHandle, Request) → event channel`, where `Request` = `{messages, tools []ToolSpec, effort, max_tokens, structured: *JSONSchema, cache_hints}`.

Events (normalized across providers — agora never sees provider wire formats):
- `text_delta {s}` / `reasoning_delta {s}`
- `tool_call {id, name, args_json}` — complete calls; bridle assembles streamed arg fragments. Parallel calls emitted in order received.
- `usage {input, output, cached, reasoning}` — final, per request; required (budget/`/status`/token displays).
- `done {stop_reason: end|tool_calls|max_tokens|refusal}` / `error {class}`.
- Cancellation via ctx — must abort the upstream request promptly (Esc-interrupt path).

## 3. Normalization duties (bridle-side)

- **ToolSpec in = one format** (name, description, JSON-schema params); bridle translates to each provider's function-calling shape and back. Tool names are agora's (incl. `mcp__server__tool`); bridle must not remangle.
- **Structured output**: when `structured` is set, bridle uses the provider's native json-schema mode if capable, else forces a single-tool call with that schema and unwraps. Guarantees: result validates or `error{class: schema}` (agora retries — workflow `schema=` contract).
- **Effort translation**: ladder is `low|medium|high|xhigh|max` (the real Claude surface); bridle maps to each provider's reasoning params/thinking config **per model** (Fable 5 thinking is always-on — omit the param; Opus 4.8/Sonnet 5 need `{type:"adaptive"}` explicit; `budget_tokens` is gone on current models — do not emit it). Unsupported tier or knob → drop with a `warning` event once per session (TUI shows it). **agora default effort = `high`** (operator: [[feedback-effort-prefer-high]] — xhigh's token cost isn't worth the marginal gain); `xhigh`/`max` are available but opt-in per stage/override, never the default.
- **Refusal handling**: bridle's Anthropic lane surfaces `stop_reason: "refusal"` (HTTP 200 on Fable 5) as its own error class (`refusal`, distinct from `auth`/`rate_limit` in §3) so agora's approval/retry logic can branch on it; where the provider supports a server-side `fallbacks` param (Fable 5 → Opus 4.8), bridle may apply it transparently and report which model served via `usage`.
- **Error taxonomy**: `auth | rate_limit | overloaded | context_length | schema | network | refusal | provider` — agora's retry policy keys off the class (rate_limit/overloaded/network retryable with backoff; auth surfaces immediately; context_length routes to the context manager; refusal is non-retryable content, surfaced to the turn/approval layer).
- **Role translation**: agora composes with three abstract roles (system/developer/user — the authority gradient, agora-spec-prompt §1a); bridle maps them per provider (native developer role where it exists; developer-role fragments become a post-core system block on Anthropic-shaped APIs). Ordering within a role is preserved.
- **Prompt caching**: `cache_hints` marks the stable prefix (system + tools + skills catalog); bridle applies provider cache controls where supported, no-op otherwise.

## 4. Non-requirements (explicitly agora-side)

History assembly, compaction, tool execution, approvals, skills/AGENTS.md injection, session persistence — bridle sees only the final `messages` + `tools` per request. bridle stays a *model* funnel, not a harness.

## 5. Gap checklist against current bridle (to verify, not assumed)

context_window/pricing in registry metadata · structured-output forcing on non-native providers · streamed tool-arg assembly · effort translation coverage per lane · error-class normalization · cancellation latency · **prompt-dialect metadata + `system_prompt_mode` in the registry (agora-spec-prompt §4)** · **role translation (developer-role mapping per provider, agora-spec-prompt §1a)**. Each unverified item is a bridle ticket before the dependent agora feature ships.
