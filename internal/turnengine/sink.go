package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// itemPayload is the payload shape item.* events carry for agent_message/
// reasoning — matches internal/io/pipe.go's itemPayload shape byte-for-byte
// (that type is unexported to internal/io, so this is a local copy of the
// wire shape, not a reused Go type; contracts/testdata/flows/turn.jsonl is
// the frozen fixture both packages independently target).
type itemPayload struct {
	Text string `json:"text"`
}

// usagePayload is the turn.completed event body — matches
// conformance/helpers.go's usagePayload shape (same reuse-the-SHAPE note).
type usagePayload struct {
	Usage contracts.Usage `json:"usage"`
}

// turnFailedPayload is the turn.failed event body — matches
// internal/io/pipe.go's turnFailedPayload shape: {"interrupted": bool} is
// the ENGINE's authoritative signal that a failure was an interruption
// rather than a genuine error (see pipe.go's doc comment on why this can't
// be inferred client-side).
type turnFailedPayload struct {
	Interrupted bool `json:"interrupted"`
}

// errorPayload is the payload shape carried by an EvError event — matches
// internal/io/scripted_engine.go's errorPayload shape.
type errorPayload struct {
	Message string `json:"message"`
}

// rateLimitPayload is the EvRateLimit event body — wraps contracts.RateLimit
// under a "rate_limit" key, the same nest-under-the-name-of-the-thing shape
// usagePayload above uses for contracts.Usage.
type rateLimitPayload struct {
	RateLimit contracts.RateLimit `json:"rate_limit"`
}

// Tool-call item payload shapes (U-C5, NEX-784) — mirror
// agora-engine-blueprint.md's approval-payload-shape convention
// (docs/spec/DEVIATIONS.md §5) but for item.* events rather than
// approval.requested: a fixed, documented wire shape per contracts.ItemType
// so a future TUI renderer (U-C5b) has something stable to target. See
// docs/spec/DEVIATIONS.md's new §11 entry for the full rationale.

// commandExecStartedPayload is item.started's body for
// contracts.ItemCommandExecution — Command is either the actual shell
// command (run_command) or a synthesized "<tool> <key arg>" summary for
// every other tool routed here (reads, and any unrecognized tool name).
type commandExecStartedPayload struct {
	Command string `json:"command"`
}

// commandExecCompletedPayload is item.completed's body for
// contracts.ItemCommandExecution.
type commandExecCompletedPayload struct {
	Command string `json:"command"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
}

// fileChangeStartedPayload is item.started's body for
// contracts.ItemFileChange (write_file/edit_file). The full diff is heavy —
// path + status is the v1 shape (per the brief); a richer payload is a
// later ticket.
type fileChangeStartedPayload struct {
	Path string `json:"path"`
}

// fileChangeCompletedPayload is item.completed's body for
// contracts.ItemFileChange.
type fileChangeCompletedPayload struct {
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
}

// mcpToolCallStartedPayload is item.started's body for
// contracts.ItemMCPToolCall. Args rides through as the tool call's raw
// JSON, unmodified — no schema to decode it against here.
type mcpToolCallStartedPayload struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// mcpToolCallCompletedPayload is item.completed's body for
// contracts.ItemMCPToolCall.
type mcpToolCallCompletedPayload struct {
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

// toolCallState is what emitToolStart records under s.mu, keyed by the
// bridle ToolCallStart/ToolCallResult correlation ID, so the matching
// ToolCallResult (which carries no Name/Args of its own — see bridle's
// events.go) can still build the right item.completed shape: seq/itemType
// were decided once at Start time (from the tool name), and summary is
// whichever identifying string that item kind's completed payload needs
// (the command-execution command summary, the file_change path, or the
// mcp_tool_call tool name — see emitToolStart).
type toolCallState struct {
	seq      int64
	itemType contracts.ItemType
	summary  string
}

// toolCallTiming records the EVENT time (NEX-796) a tool call's Start and
// Result were actually observed by this sink — the real wall time a
// multi-step turn's tool calls happened, not the turn-boundary batch
// stamp persistTurn used to apply uniformly to every item. Result stays
// the zero Time until emitToolResult fires (never, for a tool call an
// interrupted turn never got a result for).
type toolCallTiming struct {
	Start  time.Time
	Result time.Time
}

// mcpToolPrefix mirrors internal/mcp.ToolNamePrefix ("mcp__") without
// importing internal/mcp — same rationale/convention as
// internal/toolrunner/classify.go's own mcpPrefix constant.
const mcpToolPrefix = "mcp__"

// itemTypeForTool maps a bridle tool-call Name onto the contracts.ItemType
// its item.* events should carry: run_command -> ItemCommandExecution;
// write_file/edit_file -> ItemFileChange; mcp__-prefixed -> ItemMCPToolCall;
// everything else (the read-only fs tools: read_file/list_dir/glob/grep,
// and any unrecognized tool name) falls back to ItemCommandExecution as a
// generic "a tool ran" display — there is no dedicated ItemType for
// read-only tool activity in the contracts vocabulary yet, and
// command_execution's shape (a readable command string) fits a synthesized
// "read_file <path>"-style summary well enough for v1 (see
// toolCommandSummary).
func itemTypeForTool(name string) contracts.ItemType {
	switch name {
	case toolrunner.ToolRunCommand:
		return contracts.ItemCommandExecution
	case toolrunner.ToolWriteFile, toolrunner.ToolEditFile,
		toolrunner.LegacyWriteFile, toolrunner.LegacyEditFile:
		return contracts.ItemFileChange
	case contracts.ToolPlan:
		return contracts.ItemPlan
	case toolrunner.ToolAgent:
		// agent() gets its OWN item type rather than falling through to
		// command_execution (agora#155). A subagent is the longest-running
		// thing a turn can do — a foreground spawn blocks the parent's tool
		// call for the child's whole lifetime — and it was the one tool with
		// no distinguishable wire signal at all. During the agora#152
		// incident the operator's entire view of a deadlocked child was the
		// generic spinner for 30+ minutes.
		//
		// contracts.ItemAgentSpawn was already defined and registered in the
		// contracts known-items test with zero production emitters; this
		// wires it rather than inventing a shape.
		return contracts.ItemAgentSpawn
	default:
		if strings.HasPrefix(name, mcpToolPrefix) {
			return contracts.ItemMCPToolCall
		}
		return contracts.ItemCommandExecution
	}
}

// toolCommandSummary builds the readable "command" string for a
// command_execution item: run_command's actual shell command, or for
// every other tool routed here (a read, or an unrecognized name) a
// synthesized "<tool> <key arg>" summary (the key arg being whichever of
// path/pattern Args best-effort-decodes to). Args decode failure — or a
// tool with neither key — falls back to the bare tool name; this never
// panics (mustMarshal's "programmer error" contract only covers the
// locally-built payload struct, not decoding a model-supplied Args blob).
func toolCommandSummary(name string, args json.RawMessage) string {
	if name == toolrunner.ToolRunCommand {
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &a); err == nil && a.Command != "" {
			return a.Command
		}
		return name
	}
	var a struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"` // legacy
		Pattern  string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &a); err == nil {
		if p := firstNonEmptyArg(a.FilePath, a.Path); p != "" {
			return name + " " + p
		}
		if a.Pattern != "" {
			return name + " " + a.Pattern
		}
	}
	return name
}

// toolArgPath best-effort-decodes Args' "path" field for a file_change
// item (write_file/edit_file both carry one). Decode failure or a missing
// field returns "" — never panics, same rationale as toolCommandSummary.
func toolArgPath(args json.RawMessage) string {
	var a struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"` // legacy
	}
	_ = json.Unmarshal(args, &a)
	return firstNonEmptyArg(a.FilePath, a.Path)
}

// firstNonEmptyArg prefers the advertised file_path over the legacy path.
func firstNonEmptyArg(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// toolResultText best-effort-renders a ToolCallResult.Result blob as
// display text for command_execution's "output" field: toolrunner results
// are conventionally a JSON-encoded string (see surfacerunner.go), so
// unmarshal-as-string is tried first (giving clean unquoted text); a
// non-string Result (or decode failure) falls back to the raw JSON bytes
// verbatim rather than dropping it.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// turnSink implements bridle.EventSink for one in-flight turn: it
// translates bridle's Event vocabulary into agora contracts.Event and
// writes them to out, minting item.seq deterministically (a single
// monotonic per-turn counter, shared across item kinds — matches
// contracts/testdata/flows/turn.jsonl's single agent_message item at
// seq=1).
//
// This slice's translated subset (agora-engine-blueprint.md §5's TUI event
// mapping, narrowed to what a text-only turn against bridle's fake
// provider exercises):
//
//	ModelChunk    -> item.started (first chunk) + item.agent_message.delta
//	ReasoningChunk -> item.started (first chunk) + item.updated (each chunk)
//	TurnDone      -> item.completed for whichever item(s) are open (full
//	                 text/reasoning content) — turn.completed itself is
//	                 emitted by Manager from the RETURNED TurnResult, not
//	                 from this event, because bridle's own abort path
//	                 (run.go partialAbort*) returns StopReasonAborted
//	                 WITHOUT ever emitting TurnDone — so TurnDone is not a
//	                 reliable turn-boundary signal on its own.
//	TurnError/Warning -> error{message}
//	ToolCallStart/ToolCallResult -> item.started/item.completed, keyed by
//	                 the call ID so Start and Result share one item.seq
//	                 (U-C5, NEX-784) — see itemTypeForTool for the tool
//	                 name -> contracts.ItemType mapping and emitToolStart/
//	                 emitToolResult for the payload shapes.
//	StepBoundary/MCPServerFailed -> no agora wire equivalent this slice;
//	                 dropped (documented, not silently forgotten: neither
//	                 has a contracts.EventType to land on yet).
//
// Note on the interrupted case: an aborted turn never reaches emitDone
// (bridle's abort path returns without emitting TurnDone — see the
// TurnDone bullet above), so an item.started with no matching
// item.completed is a normal, expected artifact of interruption, not a
// bug — there is no item-cancelled event in the contracts vocabulary.
// turn.failed{interrupted:true} (emitted by Manager, not this sink) is
// the terminal signal a consumer should key off of; it does not imply
// every open item was cleanly closed out.
type turnSink struct {
	threadID string
	turnID   string
	out      chan<- contracts.Event
	ctx      context.Context // gates delivery; the OUTER Run-level ctx, not the interrupt-scoped turn ctx — see manager.go's runOneTurn doc comment for why.
	// clock is the item-creation-time source (NEX-796): nil means
	// time.Now().UTC() (see now()); Manager passes its own m.now so
	// WithClock(...) test overrides reach the sink too.
	clock func() time.Time

	mu       sync.Mutex
	seq      int64
	agentSeq int64 // 0 = agent_message item not yet started
	agentBuf strings.Builder
	reasSeq  int64 // 0 = reasoning item not yet started
	reasBuf  strings.Builder
	// toolSeq correlates a tool call's ID (bridle.ToolCallStart/
	// ToolCallResult share one) to the item.seq/type/summary emitToolStart
	// minted for it, so emitToolResult (which gets no Name/Args of its
	// own) can build the matching item.completed. Lazily initialized —
	// most turns never call a tool.
	toolSeq map[string]toolCallState
	// toolTimes records each tool call's real Start/Result event time
	// (NEX-796), keyed by the same correlation ID as toolSeq — but never
	// deleted (unlike toolSeq, which is one-shot), since persistTurn reads
	// this AFTER the turn completes, once bridle.TurnResult.ToolCalls is
	// available to loop over by ID.
	toolTimes map[string]*toolCallTiming
	// doneAt is the event time emitDone observed for bridle.TurnDone — the
	// real moment the turn's closing text became available, used as the
	// closing TIAgentMessage/TITurnUsage item's ts. Zero until emitDone
	// fires (never, for an aborted/errored turn — see emitDone's doc
	// comment on TurnDone not being a reliable turn-boundary signal).
	doneAt time.Time
}

func newTurnSink(threadID, turnID string, out chan<- contracts.Event, ctx context.Context, clock func() time.Time) *turnSink {
	return &turnSink{threadID: threadID, turnID: turnID, out: out, ctx: ctx, clock: clock}
}

// now returns the sink's item-creation-time source: the injected clock
// (Manager.now, itself WithClock-overridable) when set, time.Now().UTC()
// for direct-construction call sites (e.g. sink_test.go) that pass nil.
func (s *turnSink) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now().UTC()
}

// toolCallEventTimes returns the recorded (start, result) event times for
// a tool call ID (NEX-796), or the zero values and ok=false if this sink
// never observed a ToolCallStart for id — bridle always emits Start before
// Result for the same ID (see emitToolResult's doc comment), so persistTurn
// should never hit the not-ok case for an ID present in TurnResult.ToolCalls;
// it's handled defensively rather than assumed. Result is the zero Time
// (separately, ok stays true) when Start fired but no matching Result ever
// did — an interrupted turn's in-flight tool call.
func (s *turnSink) toolCallEventTimes(id string) (start, result time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.toolTimes[id]
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return t.Start, t.Result, true
}

// turnDoneEventTime returns the event time emitDone recorded for this
// turn's TurnDone (NEX-796), or the zero Time if TurnDone never fired.
func (s *turnSink) turnDoneEventTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doneAt
}

var _ bridle.EventSink = (*turnSink)(nil)

// Emit implements bridle.EventSink.
func (s *turnSink) Emit(e bridle.Event) {
	switch ev := e.(type) {
	case bridle.ModelChunk:
		s.emitChunk(ev.Text, &s.agentSeq, &s.agentBuf, contracts.ItemAgentMessage, contracts.EvAgentMessageDelta)
	case bridle.ReasoningChunk:
		s.emitChunk(ev.Text, &s.reasSeq, &s.reasBuf, contracts.ItemReasoning, contracts.EvItemUpdated)
	case bridle.TurnDone:
		s.emitDone(ev)
	case bridle.TurnError:
		// bridle marks some TurnErrors as structured always-benign signals
		// (events.go: Retry, ProviderAPIError, ResumeFallback) that arrive
		// ALONGSIDE a successful TurnDone. Rendering those as agora's red
		// "error:" status (model.go) makes a turn that actually succeeded look
		// broken — but DROPPING them told the operator nothing, which is worse
		// for the resume fallback: the turn quietly restarts on a fresh
		// provider session and the prior context is gone with nothing in the
		// stream saying so (agora#120). They go out as EvWarning instead.
		// Everything else — including StderrOutput's arbitrary sidecar stderr,
		// which can carry real failures — still surfaces as an error (see
		// isNonTerminalErrorStage).
		if isNonTerminalErrorStage(ev.Stage) {
			s.emitWarning(ev.Err, string(ev.Stage))
			return
		}
		msg := ""
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		s.send(contracts.Event{Type: contracts.EvError, Payload: mustMarshal(errorPayload{Message: msg})})
	case bridle.Warning:
		// A bridle.Warning is, by name, non-fatal — never an agora "error:",
		// but no longer dropped either: same reasoning as the non-terminal
		// TurnError stages above.
		s.emitWarningMsg(ev.Message, ev.Kind)
		return
	case bridle.ToolCallStart:
		s.emitToolStart(ev)
	case bridle.ToolCallResult:
		s.emitToolResult(ev)
	case bridle.RateLimit:
		s.emitRateLimit(ev)
	default:
		// bridle.StepBoundary/MCPServerFailed: see the type doc comment
		// above — no agora wire equivalent yet, out of scope this slice.
	}
}

// emitWarning sends a non-terminal note. A nil/empty error emits nothing:
// a warning with no message is noise, and the caller cannot always know
// whether the source populated one.
func (s *turnSink) emitWarning(err error, stage string) {
	if err == nil {
		return
	}
	s.emitWarningMsg(err.Error(), stage)
}

// emitWarningMsg is emitWarning for sources that carry a plain string
// rather than an error (bridle.Warning). An empty message emits nothing:
// a note with no content is noise.
func (s *turnSink) emitWarningMsg(msg, stage string) {
	if msg == "" {
		return
	}
	s.send(contracts.Event{
		Type:    contracts.EvWarning,
		Payload: mustMarshal(contracts.WarningPayload{Message: msg, Stage: stage}),
	})
}

// isNonTerminalErrorStage reports whether a bridle.TurnError.Stage is a
// STRUCTURED, always-benign bridle signal (events.go) that must NOT render as
// agora's terminal "error:" status — the turn still produces a TurnDone.
//
// StderrOutput is deliberately NOT in this set. It is the "arbitrary sidecar
// stderr on a clean exit" channel — it carries UNKNOWN content, which includes
// real failures that happen to exit 0 (e.g. "not authenticated"): the first
// live turn with missing creds produced exactly that, and suppressing
// StderrOutput turned an auth failure into a silent no-op ("no error, no
// response"). Benign every-turn node noise (CLAUDE_SDK_CAN_USE_TOOL_SHADOWED)
// is instead silenced at the source via NODE_NO_WARNINGS (main.go), so whatever
// still reaches StderrOutput is worth surfacing. Terminal stages (harness-recover,
// provider, subprocess_exit, stream_truncated, subprocess_exit_partial) are
// likewise absent so they keep surfacing as errors.
func isNonTerminalErrorStage(stage bridle.TurnErrorStage) bool {
	switch stage {
	case bridle.TurnErrorStageRetry,
		bridle.TurnErrorStageProviderAPIError,
		bridle.TurnErrorStageResumeFallback:
		return true
	}
	return false
}

// emitToolStart translates a bridle.ToolCallStart into item.started: mint
// an item.seq (the same monotonic s.seq counter emitChunk uses — tool-call
// items share the turn's item sequence), classify the tool name into a
// contracts.ItemType (itemTypeForTool), build that type's started payload,
// and record a toolCallState keyed by ev.ID so the matching
// ToolCallResult reuses the SAME seq/type (emitToolResult).
func (s *turnSink) emitToolStart(ev bridle.ToolCallStart) {
	itemType := itemTypeForTool(ev.Name)

	var summary string
	var payload json.RawMessage
	switch itemType {
	case contracts.ItemFileChange:
		summary = toolArgPath(ev.Args)
		payload = mustMarshal(fileChangeStartedPayload{Path: summary})
	case contracts.ItemMCPToolCall:
		summary = ev.Name
		payload = mustMarshal(mcpToolCallStartedPayload{Tool: ev.Name, Args: ev.Args})
	case contracts.ItemAgentSpawn:
		summary = agentSpawnSummary(ev.Args)
		payload = mustMarshal(agentSpawnStartedPayload{AgentType: summary})
	case contracts.ItemPlan:
		// planning-questions §7 / contracts/testdata/flows/plan_gate.jsonl:
		// item.started carries NO payload for plan (the artifact only
		// appears once, on item.completed, below) — summary stashes the
		// original args (already a marshaled contracts.PlanArtifact) so
		// emitToolResult can echo it back without re-deriving it from a
		// ToolCallResult that never carries the artifact itself (our
		// Deny+Result short-circuit's Result is a plain confirmation
		// string, not the artifact — see turnengine/approval.go).
		summary = string(ev.Args)
	default: // contracts.ItemCommandExecution
		summary = toolCommandSummary(ev.Name, ev.Args)
		payload = mustMarshal(commandExecStartedPayload{Command: summary})
	}

	s.mu.Lock()
	s.seq++
	seq := s.seq
	if s.toolSeq == nil {
		s.toolSeq = make(map[string]toolCallState)
	}
	s.toolSeq[ev.ID] = toolCallState{seq: seq, itemType: itemType, summary: summary}
	if s.toolTimes == nil {
		s.toolTimes = make(map[string]*toolCallTiming)
	}
	s.toolTimes[ev.ID] = &toolCallTiming{Start: s.now()}
	s.mu.Unlock()

	s.send(contracts.Event{
		Type:    contracts.EvItemStarted,
		Item:    &contracts.ItemRef{Seq: seq, Type: itemType},
		Payload: payload,
	})
}

// emitToolResult translates a bridle.ToolCallResult into item.completed,
// looking up the seq/type/summary emitToolStart recorded for ev.ID. A
// Result with no matching Start (bridle always emits Start before Result
// for the same ID — see run.go's executeToolCall — so this should never
// happen in practice) is SKIPPED rather than given a fresh seq: with no
// recorded Name, there is no way to classify it into the right
// contracts.ItemType or build its payload, and a bare item.completed with
// no preceding item.started would be a wire artifact no consumer expects
// — see docs/spec/DEVIATIONS.md §11.
// emitRateLimit translates a bridle.RateLimit into EvRateLimit. Unlike
// tool-call events this carries no correlation id and needs no started/
// completed pairing — it is a single point-in-time reading, sent as-is.
func (s *turnSink) emitRateLimit(ev bridle.RateLimit) {
	rl := contracts.RateLimit{
		Status:       ev.Status,
		WindowType:   ev.WindowType,
		Utilization:  ev.Utilization,
		UsingOverage: ev.UsingOverage,
	}
	if !ev.ResetsAt.IsZero() {
		resetsAt := ev.ResetsAt
		rl.ResetsAt = &resetsAt
	}
	s.send(contracts.Event{Type: contracts.EvRateLimit, Payload: mustMarshal(rateLimitPayload{RateLimit: rl})})
}

func (s *turnSink) emitToolResult(ev bridle.ToolCallResult) {
	s.mu.Lock()
	state, ok := s.toolSeq[ev.ID]
	if ok {
		delete(s.toolSeq, ev.ID) // one-shot: a replayed/duplicate Result for the same ID must not reuse stale state
	}
	if t, tok := s.toolTimes[ev.ID]; tok {
		t.Result = s.now()
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	var payload json.RawMessage
	switch state.itemType {
	case contracts.ItemFileChange:
		payload = mustMarshal(fileChangeCompletedPayload{Path: state.summary, Error: ev.Err})
	case contracts.ItemMCPToolCall:
		result := ev.Result
		if result == nil {
			result = json.RawMessage(`null`)
		}
		payload = mustMarshal(mcpToolCallCompletedPayload{Tool: state.summary, Result: result, Error: ev.Err})
	case contracts.ItemAgentSpawn:
		// Agent-shaped, not command-shaped: falling through to the default
		// would emit a commandExecCompletedPayload (with the agent type
		// sitting in a "command" field) under an agent_spawn item type, so a
		// consumer decoding by item type would find the wrong shape. Result
		// carries the child's final message, which IS the tool's output.
		payload = mustMarshal(agentSpawnCompletedPayload{
			AgentType: state.summary, Result: toolResultText(ev.Result), Error: ev.Err,
		})
	case contracts.ItemPlan:
		// item.completed's payload IS the plan artifact verbatim (matching
		// contracts/testdata/flows/plan_gate.jsonl) — state.summary is the
		// original tool-call args stashed at Start time (see
		// emitToolStart), already valid JSON.
		if state.summary != "" {
			payload = json.RawMessage(state.summary)
		}
	default: // contracts.ItemCommandExecution
		payload = mustMarshal(commandExecCompletedPayload{Command: state.summary, Output: toolResultText(ev.Result), Error: ev.Err})
	}

	s.send(contracts.Event{
		Type:    contracts.EvItemCompleted,
		Item:    &contracts.ItemRef{Seq: state.seq, Type: state.itemType},
		Payload: payload,
	})
}

// emitChunk handles the shared ModelChunk/ReasoningChunk shape: mint an
// item.seq and emit item.started on the first chunk seen for that item
// kind, then emit deltaType (item.agent_message.delta for text,
// item.updated for reasoning — bridle has no dedicated reasoning-delta
// wire type yet) for every chunk including the first.
func (s *turnSink) emitChunk(text string, itemSeq *int64, buf *strings.Builder, itemType contracts.ItemType, deltaType contracts.EventType) {
	s.mu.Lock()
	first := *itemSeq == 0
	if first {
		s.seq++
		*itemSeq = s.seq
	}
	buf.WriteString(text)
	seq := *itemSeq
	s.mu.Unlock()

	if first {
		s.send(contracts.Event{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Seq: seq, Type: itemType}})
	}
	s.send(contracts.Event{
		Type:    deltaType,
		Item:    &contracts.ItemRef{Seq: seq, Type: itemType},
		Payload: mustMarshal(itemPayload{Text: text}),
	})
}

// emitDone closes out whichever item(s) this turn opened, with the full
// accumulated content (preferring bridle's own authoritative
// TurnResult.FinalText for the agent_message item when non-empty, since
// that's the harness's settled answer — see ProviderResult.FinalText's
// doc comment on draft-vs-final text; the accumulated buffer is the
// fallback for providers/tests that never populate FinalText).
func (s *turnSink) emitDone(ev bridle.TurnDone) {
	s.mu.Lock()
	s.doneAt = s.now()
	agentSeq, reasSeq := s.agentSeq, s.reasSeq
	agentText, reasText := s.agentBuf.String(), s.reasBuf.String()
	s.mu.Unlock()

	if agentSeq != 0 {
		text := agentText
		if ev.Result.FinalText != "" {
			text = ev.Result.FinalText
		}
		s.send(contracts.Event{
			Type:    contracts.EvItemCompleted,
			Item:    &contracts.ItemRef{Seq: agentSeq, Type: contracts.ItemAgentMessage},
			Payload: mustMarshal(itemPayload{Text: text}),
		})
	}
	if reasSeq != 0 {
		s.send(contracts.Event{
			Type:    contracts.EvItemCompleted,
			Item:    &contracts.ItemRef{Seq: reasSeq, Type: contracts.ItemReasoning},
			Payload: mustMarshal(itemPayload{Text: reasText}),
		})
	}
}

// send stamps thread/turn ids and delivers ev to out, backing off if ctx
// (the OUTER Run-level ctx) is done — mirrors internal/io/scripted_engine.go's
// sendEvent helper.
func (s *turnSink) send(ev contracts.Event) {
	ev.ThreadID = s.threadID
	ev.TurnID = s.turnID
	select {
	case s.out <- ev:
	case <-s.ctx.Done():
	}
}

// mustMarshal marshals a locally-defined, well-typed payload struct. A
// failure here is a programmer error — mirrors internal/io's own
// mustMarshal convention (same rationale: panicking surfaces the bug
// immediately instead of silently emitting a malformed event).
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("turnengine: marshal payload: %v", err))
	}
	return b
}

// agentSpawnStartedPayload is the item.started payload for an agent() call.
// Deliberately just the agent type: the prompt is often thousands of tokens
// and every consumer here is a live display, not an archive — the child's
// own thread holds the full prompt.
type agentSpawnStartedPayload struct {
	AgentType string `json:"agent_type"`
}

// agentSpawnCompletedPayload is the item.completed payload for an agent()
// call: which agent ran, what it returned, and any failure.
type agentSpawnCompletedPayload struct {
	AgentType string `json:"agent_type"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// agentSpawnSummary pulls the agent type out of an agent() call's args,
// defaulting to the same "general-purpose" the toolrunner defaults to so the
// display never shows an empty type.
func agentSpawnSummary(args json.RawMessage) string {
	var a struct {
		AgentType string `json:"agent_type"`
	}
	if err := json.Unmarshal(args, &a); err == nil && a.AgentType != "" {
		return a.AgentType
	}
	return "general-purpose"
}
