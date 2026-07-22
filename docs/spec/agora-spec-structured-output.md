# agora spec — structured (schema-forced) output, end to end

Closes the `structured` gap: `agent()` / `ctx.agent(schema=...)` returns a **validated object**
or a typed failure — never unparsed prose. Recon 2026-07-22 mapped the existing scaffolding;
this chapter is the build contract for the three missing links. Everything cited exists on
main today (agora `4f06ad6`-era, bridle `eaed1b9`-era); re-verify line numbers before editing.

## 0. What already exists (do not rebuild)

- **Spec contract** (agora-spec-bridle.md §2/§3): `Request.structured: *JSONSchema`; "native
  json-schema mode if capable, else force a single-tool call with that schema and unwrap.
  Guarantees: result validates or `error{class: schema}`." Error class `schema` is declared in
  BOTH repos (`bridle events.go ErrorClassSchema`, agora `contracts/model.go:87 ErrSchema`)
  and produced by NOTHING — this build makes them real.
- **agora side, wired end-to-end down to the runner seam**: `ctx.agent(schema=...)`
  (internal/workflow/ctx.go:77-134) → `AgentCallOpts.Schema` → `SubagentInvoker.InvokeAgent`
  (subagent_adapter.go:29-49) → `subagent.SpawnOpts.Schema` (manager.go:63-64) →
  `RunRequest.Schema` + the retry loop `runWithSchemaRetry` (schema.go:89-111,
  `MaxSchemaRetries = 3`, mismatch feedback per attempt, `ErrSchemaGiveUp` after cap).
- **bridle lanes**: openai lane natively lowers `ResponseFormat` incl. strict `json_schema`
  (openai.go:136-137, 487-518); deepseek degrades `json_schema`→`json_object` (deepseek.go:56-76,
  NEX-587 — keep); claude/bedrock/gemini/ollama/claudesdk/claudecode ignore it.
- **claudesdk native seam, unused**: the vendored Agent SDK supports
  `Options.outputFormat = {type:'json_schema', schema}` (sdk.d.ts:1676-1687, 899-901). The
  sidecar never sets it (index.ts:229-263). `extra_opts` (wire.go:79-81 → index.ts
  Object.assign) can force it provider-wide today — the build makes it per-request.
- **Forced-tool machinery to ride**: named-tool `ToolChoice` forcing already works on
  claude (claude.go:121-122, 348-373) and bedrock (bedrock.go:196-204, 489-499).
- **Validator with zero new deps**: `santhosh-tekuri/jsonschema/v6` is already transitive in
  both go.mod graphs; promote to direct. Replaces agora's self-described "minimal subset"
  validator (internal/subagent/schema.go:16-60).

## U-SO1 (bridle): lower `Structured` per lane — native, sidecar, forced-tool fallback

1. `lowerStreamToProviderRequest`/`lowerStreamToTurnRequest` (stream.go:284-286 — the
   documented "T4 follow-up" drop site) lower `Request.Structured` into
   `ProviderRequest/TurnRequest.ResponseFormat{Type: json_schema, Schema}`. `TurnRequest`
   callers (agora's turnengine) may also set `ResponseFormat` directly — both paths converge.
2. **claudesdk lane**: add `output_format` to the sidecar init wire (wire.go + wire.ts, same
   omitempty pattern as `effort`) and set the SDK's `Options.outputFormat` in index.ts.
   Per-request, from `ProviderRequest.ResponseFormat`. tsc-typecheck against the deployed
   node_modules (symlink trick, see the effort unit's precedent).
3. **Forced-tool fallback** for lanes with tools-but-no-native-schema (claude direct API,
   bedrock; gemini may use native `responseSchema` instead — registry_models.toml:256 says
   native, wire it natively): when `ResponseFormat.Type == json_schema` and the lane lacks
   native support, the PROVIDER (not the harness) injects one synthetic tool
   `structured_output` whose InputSchema IS the schema, sets ToolChoice to that name, and
   unwraps the tool call's args as `FinalText` (the JSON object, serialized). One round; the
   model calling anything else or nothing → `error{class: schema}`.
4. Lanes with neither native nor tools (ollama today, claudecode): return
   `error{class: schema, message: "structured output unsupported on <lane>"}` — loud, not
   silent prose (spec guarantee). `ModelCapabilities.StructuredOutput` flags updated to truth.
5. `ErrorClassSchema` is emitted for: unsupported lane, non-validating final output (where the
   provider validates), forced-tool non-compliance.

Acceptance (observable): per-lane wire tests pinning ACTUAL request bytes — openai body carries
`response_format.json_schema` (exists, keep green); sidecar init line carries `output_format`
when set and omits when empty (fake-sidecar echo pattern, claudesdk_effort_test.go precedent);
claude/bedrock request carries the synthetic tool + forced tool_choice; ollama returns
class=schema. `CGO_ENABLED=0 go test ./...` green (known claudecode live-CLI failure exempt).

## U-SO2 (agora): enginerunner honors `req.Schema` + real validation

1. `internal/subagent/enginerunner/enginerunner.go:140-146` — implement the documented cut:
   when `req.Schema != nil`, the child turn's request carries it (TurnRequest.ResponseFormat
   via a new turnengine option or per-turn field — pick the smallest seam; runOneTurn builds
   TurnRequest at manager.go:~919). `req.Feedback` (attempt > 1) is appended to the child
   prompt (the retry loop already composes feedback text — schema.go).
2. Upgrade `validateStructured` (schema.go) to `santhosh-tekuri/jsonschema/v6` (draft
   2020-12), promoted to a direct dependency in agora's go.mod. Keep the retry-loop semantics
   and `ErrSchemaGiveUp` unchanged. Compile the schema once per spawn, not per attempt.
3. Produce `contracts.ErrSchema` on give-up where the error surfaces on the wire.

Acceptance: e2e test — `ctx.agent(schema=...)`-shaped spawn against a fake provider scripted
to return (attempt 1) invalid JSON then (attempt 2) valid → returns the DECODED object,
attempt count = 2, feedback visible in attempt 2's captured request; a never-valid script →
ErrSchemaGiveUp after exactly MaxSchemaRetries with class=schema. A workflow .star fixture
using `ctx.agent(schema=...)` passes through `agora workflow run` (extends the existing
cmd/agora workflow e2e).

## U-SO3 (agora): `schema` on the native agent() tool

`internal/toolrunner/agent.go:65-70` — add `schema` (object) to `agentArgs` + the tool's
InputSchema; thread into `SpawnOpts.Schema`. The tool result becomes the validated JSON
(serialized) instead of plain text when schema is set. Depth guard, approval kind, and
synchronous fire-and-collect semantics unchanged.

Acceptance: fake-provider turn where the parent calls
`agent({prompt, schema})` → tool result is the child's validated JSON; invalid-forever child →
tool error mentioning schema after retries; existing no-schema tests unchanged.

## Build order & notes

U-SO1 → U-SO2 (dep bump) → U-SO3. Follow the repo's standing rules: CGO_ENABLED=0, gofmt,
no `go mod tidy`, no POSIX-shell test fixtures, HOME+USERPROFILE isolation in tests, close
every file handle (Windows CI is the leak detector). deepseek's json_object degrade and the
existing `extra_opts` escape hatch remain untouched.
