package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/CarriedWorldUniverse/agora/internal/hooks"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// This file is the seam between HookRunner (hookrunner.go: discovery,
// trust, dispatch — the mechanics) and the Manager/bridle.Harness turn path
// (WHERE each of the 10 events fires). Scope, per the ticket:
//
//   - PreToolUse / PermissionRequest: approval.go's beforeToolCall, before
//     and around approval.Decide respectively (see beforeToolCall's own
//     doc comment for the exact ordering and the invariant this preserves).
//   - PostToolUse: afterToolCallHook below, registered on every harness
//     (NewManager + harnessFor) via bridle's RegisterAfterToolCall.
//   - SessionStart: fireSessionStart, called once at the top of Run.
//   - UserPromptSubmit: fireUserPromptSubmit, called from Run's
//     InUserMessage case, right after turnID is minted (matches the
//     approval hook's own turnID-minted-by-Run discipline — see
//     manager.go's InUserMessage doc comment).
//   - Stop: fired from runOneTurn's success path (StopReasonModelDone/
//     MaxSteps) — the closest live analogue to "the agent finished
//     responding" this engine has; see fireStop's doc comment for why the
//     aborted/error paths do NOT fire Stop (DEVIATIONS.md).
//   - PreCompact/PostCompact/SubagentStart/SubagentStop: NOT fired — no
//     compaction or subagent machinery exists on this engine yet (spec
//     scope note, ticket item 7). A future compaction/subagent unit fires
//     them through the SAME HookRunner.Fire* pattern this file establishes.
//
// Every entry point below is m.hookRunner == nil safe at the call site (not
// just inside HookRunner's own methods) so the hot path never even builds a
// stdin payload when hooks are disabled.

// postToolUseResponsePayload is this unit's tool_response wire shape for
// PostToolUse's stdin (spec §2.3): the tool's raw JSON result plus its
// error string, mirroring toolResultItemPayload's {result, err} shape
// (manager.go) rather than inventing a third shape for the same data.
type postToolUseResponsePayload struct {
	Result json.RawMessage `json:"result"`
	Err    string          `json:"err,omitempty"`
}

// afterToolCallHook is the bridle.Hook[bridle.AfterToolCallCtx] registered
// on every harness this Manager builds (NewManager's default harness,
// harnessFor's alt-provider harnesses) — PostToolUse fires here, AFTER a
// tool call has resolved (executed for real, or short-circuited by
// beforeToolCall's Deny — bridle's executeToolCall fires AfterToolCall on
// BOTH paths, see hooks.go's doc comment in the bridle module).
//
// c.Result IS what becomes the tool_result message the model sees next
// (bridle's run.go builds toolMsg from atc.Result after this hook runs) —
// so a PostToolUse block's feedback (spec §2.3: "reason, feedback to
// model") is applied by folding it into c.Result.Err, not by inventing a
// side channel. continue:false (Stopped) ends the turn via HookAbort — the
// nearest bridle primitive to spec's "stops turn with reason" (the reason
// itself isn't separately surfaced past the abort; runOneTurn's aborted
// path already reports turn.failed{interrupted:true}, see DEVIATIONS.md).
func (m *Manager) afterToolCallHook(ctx context.Context, c bridle.AfterToolCallCtx) (bridle.AfterToolCallCtx, bridle.HookAction, error) {
	if m.hookRunner == nil {
		return c, bridle.HookContinue, nil
	}
	// Same passthrough rule as beforeToolCall (approval.go): only calls
	// this Manager's own Surface executes are real tool calls with side
	// effects worth a PostToolUse audit; ctxmap's recall/inspect/read_raw
	// tools (and any other foreign name) pass through untouched.
	if !m.surface.Handles(c.Call.Name) {
		return c, bridle.HookContinue, nil
	}
	htc := m.loadHookTurn()
	if htc == nil {
		// Defensive only — see beforeToolCall's identical guard doc
		// comment. AfterToolCall firing with no hookTurn published would
		// mean this hook ran outside any turn Run started, which shouldn't
		// happen; degrade to a no-op audit skip rather than panic.
		return c, bridle.HookContinue, nil
	}

	respJSON, err := json.Marshal(postToolUseResponsePayload{Result: c.Result.Result, Err: c.Result.Err})
	if err != nil {
		return c, bridle.HookContinue, nil
	}
	agg := m.hookRunner.FirePostToolUse(ctx, m.threadID, htc.turnID, m.model, c.Call.Name, c.Call.Args, respJSON, c.Call.ID)
	if agg.Blocked {
		if c.Result.Err == "" {
			c.Result.Err = agg.Feedback
		} else {
			c.Result.Err = c.Result.Err + "; " + agg.Feedback
		}
	}
	if agg.Stopped {
		return c, bridle.HookAbort, nil
	}
	return c, bridle.HookContinue, nil
}

// fireSessionStart runs once at the top of Manager.Run (spec §2.6:
// SessionStart is thread-scoped, one per Run — see events.go's
// TurnScoped()). source mirrors the SAME resume-probe turnSession's
// first-turn New computation uses (storeHasItems), so "resume" here means
// exactly what it means there: an earlier process already ran turns on
// this thread. additionalContext folds into m.appendSystemPrompt — the
// closest existing injection point this engine has (there is no separate
// per-turn "developer message" channel yet); continue:false/block are
// logged, not honored — Run has no session-abort mechanism to hook into
// (see DEVIATIONS.md).
func (m *Manager) fireSessionStart(ctx context.Context) {
	if m.hookRunner == nil {
		return
	}
	source := "startup"
	if m.store != nil && storeHasItems(m.store, m.threadID) {
		source = "resume"
	}
	agg := m.hookRunner.FireSessionStart(ctx, m.threadID, m.model, source)
	if agg.AdditionalContext != "" {
		if m.appendSystemPrompt == "" {
			m.appendSystemPrompt = agg.AdditionalContext
		} else {
			m.appendSystemPrompt = m.appendSystemPrompt + "\n\n" + agg.AdditionalContext
		}
	}
	if agg.Blocked || agg.Stopped {
		fmt.Fprintf(os.Stderr, "turnengine: hooks: SessionStart hook requested stop (reason=%q) — not honored, Run has no session-abort mechanism yet (see DEVIATIONS.md)\n", agg.Reason)
	}
}

// fireUserPromptSubmit fires UserPromptSubmit for the user_message that is
// about to start turnID (spec §2.7). Returns (blocked, reason, text): a
// blocked prompt never starts a turn (Run's InUserMessage case forwards an
// EvError and loops, per manager.go); otherwise text is prompt with any
// additionalContext appended (the closest per-turn injection point — there
// is no separate context channel on contracts.Input).
func (m *Manager) fireUserPromptSubmit(ctx context.Context, turnID, prompt string) (blocked bool, reason, text string) {
	if m.hookRunner == nil {
		return false, "", prompt
	}
	agg := m.hookRunner.FireUserPromptSubmit(ctx, m.threadID, turnID, m.model, prompt)
	if agg.Blocked {
		return true, agg.Reason, prompt
	}
	if agg.Stopped {
		return true, agg.StopReason, prompt
	}
	if agg.AdditionalContext != "" {
		return false, "", prompt + "\n\n[hook context]\n" + agg.AdditionalContext
	}
	return false, "", prompt
}

// fireStop fires Stop for a turn that just finished successfully
// (StopReasonModelDone/MaxSteps — runOneTurn's call site). Deliberately NOT
// fired on the aborted/error paths: spec's Stop models "the agent finished
// responding" (§2.9/2.10's stop_hook_active continuation-loop machinery
// presumes a real response happened), which an interrupted or errored turn
// never did — see DEVIATIONS.md for this narrowing. Looped (a hook wants a
// continuation prompt) is logged, not honored: this engine's runOneTurn has
// already returned its terminal result by the time this fires, and
// re-entering RunTurn for a continuation round is out of this unit's scope.
func (m *Manager) fireStop(ctx context.Context, turnID, model, lastAssistantMessage string) {
	if m.hookRunner == nil {
		return
	}
	agg := m.hookRunner.FireStop(ctx, m.threadID, turnID, model, lastAssistantMessage)
	if agg.Looped {
		fmt.Fprintf(os.Stderr, "turnengine: hooks: Stop hook requested a continuation (turn already completed) — not honored (see DEVIATIONS.md): %s\n", agg.Continuation)
	}
}

// --- HookRunner.Fire* — event-specific stdin build + dispatch + aggregate ---

// common builds the shared stdin fields (spec §2 top) for one event firing.
// permission_mode reports the approval posture actually in force — the
// builtin preset name, "sandbox-auto" for the engine's zero-config policy,
// or "custom" for an operator-defined PolicySet (see permissionmode.go).
// It remains REPORT-ONLY per agora-spec-approvals.md §3 ("hooks never
// *configure* via this field"): nothing in this engine's own decision-
// making reads it back, so what a hook is told cannot loosen or tighten
// anything.
func (hr *HookRunner) common(threadID, turnID, model, eventName string) hooks.CommonInput {
	return hooks.CommonInput{
		SessionID:      threadID,
		Cwd:            hr.cwd,
		HookEventName:  eventName,
		Model:          model,
		PermissionMode: hr.reportedPermissionMode(),
		TurnID:         turnID,
	}
}

type preToolUseInput struct {
	hooks.CommonInput
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	ToolUseID string          `json:"tool_use_id"`
}

type permissionRequestInput struct {
	hooks.CommonInput
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type postToolUseInput struct {
	hooks.CommonInput
	ToolName     string          `json:"tool_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
	ToolUseID    string          `json:"tool_use_id"`
}

type sessionStartInput struct {
	hooks.CommonInput
	Source string `json:"source"`
}

type userPromptSubmitInput struct {
	hooks.CommonInput
	Prompt string `json:"prompt"`
}

type stopInput struct {
	hooks.CommonInput
	StopHookActive       bool    `json:"stop_hook_active"`
	LastAssistantMessage *string `json:"last_assistant_message"`
}

// fire is the shared discover-matched -> resolve-trust -> dispatch ->
// interpret pipeline every Fire* method below drives. matchAgainst is the
// per-event "matched string" (§1.5: tool name for tool events, source for
// SessionStart, "" — ignored — for UserPromptSubmit/Stop). A stdin
// marshal failure degrades to "no handlers ran" (spec §3: "Serialization
// failure... -> all matched handlers reported Failed for that event" —
// Failed handlers contribute nothing to any Aggregate* function, so
// returning zero Outcomes here is equivalent).
func (hr *HookRunner) fire(ctx context.Context, event hooks.EventName, matchAgainst string, stdinPayload any, interpret func(int, []byte, []byte) hooks.HandlerOutcome) []hooks.Outcome {
	stdin, err := json.Marshal(stdinPayload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turnengine: hooks: marshal stdin for %s: %v\n", event, err)
		return nil
	}
	matched, warnings := hr.registry.ForEvent(event, matchAgainst)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, w)
	}
	if len(matched) == 0 {
		return nil
	}
	resolved := hooks.Resolve(matched, hr.state, hr.bypassTrust)
	// Deliberately dispatch on a DETACHED context, not the caller's ctx
	// (which is turn/session-scoped — turnCtx for tool events, Run's outer
	// ctx for SessionStart/UserPromptSubmit): a hook process's lifetime is
	// governed by its OWN configured Timeout (dispatch.go's runWithTimeout,
	// default 600s/floor 1s per spec §1.4), not by whether the turn that
	// triggered it happens to end first. This matters most for async
	// handlers (§1.4 "fire-and-forget... Go makes this trivial: goroutine +
	// no result wait") — an async handler tied to turnCtx would get killed
	// the moment Run cancels turnCtx right after reaping the turn's
	// terminal event (manager.go's turnDone case), which would silently
	// turn "doesn't block the turn" into "doesn't survive the turn either",
	// defeating the point of async for anything slower than the turn
	// itself (a notification, an audit upload, ...). Sync handlers still
	// can't outlast their own Timeout either way — this only changes
	// whether an unrelated turn-cancellation cuts them off early.
	results := hr.dispatcher.Dispatch(context.Background(), event, resolved, stdin)
	outs := make([]hooks.Outcome, 0, len(results))
	for _, r := range results {
		if r.Skipped {
			// Untrusted/disabled (never ran) or async (its outcome, if
			// any, arrives later on hr.asyncResults — see DiscoverHooks'
			// drain goroutine) — neither contributes to THIS firing's
			// synchronous aggregation.
			continue
		}
		ho := interpret(r.Result.ExitCode, r.Result.Stdout, r.Result.Stderr)
		if r.TimedOut {
			ho.Status = hooks.RunFailed
		}
		outs = append(outs, hooks.Outcome{Handler: r.Handler, CompletionIndex: r.CompletionIndex, HandlerOutcome: ho})
	}
	return outs
}

// FirePreToolUse fires PreToolUse for one tool call, matched against
// toolName (spec §1.5). Spec: docs/spec/agora-spec-hooks.md §2.1.
func (hr *HookRunner) FirePreToolUse(ctx context.Context, threadID, turnID, model, toolName string, toolInput json.RawMessage, toolUseID string) hooks.PreToolUseAggregate {
	if hr == nil {
		return hooks.PreToolUseAggregate{}
	}
	in := preToolUseInput{
		CommonInput: hr.common(threadID, turnID, model, string(hooks.EventPreToolUse)),
		ToolName:    toolName,
		ToolInput:   toolInput,
		ToolUseID:   toolUseID,
	}
	return hooks.AggregatePreToolUse(hr.fire(ctx, hooks.EventPreToolUse, toolName, in, hooks.InterpretPreToolUse))
}

// FirePermissionRequest fires PermissionRequest for one approval situation
// (runs in the approval path — approval.go's beforeToolCall — before the
// UI/Ask rendezvous). Spec §2.2.
func (hr *HookRunner) FirePermissionRequest(ctx context.Context, threadID, turnID, model, toolName string, toolInput json.RawMessage) hooks.PermissionRequestAggregate {
	if hr == nil {
		return hooks.PermissionRequestAggregate{}
	}
	in := permissionRequestInput{
		CommonInput: hr.common(threadID, turnID, model, string(hooks.EventPermissionRequest)),
		ToolName:    toolName,
		ToolInput:   toolInput,
	}
	return hooks.AggregatePermissionRequest(hr.fire(ctx, hooks.EventPermissionRequest, toolName, in, hooks.InterpretPermissionRequest))
}

// FirePostToolUse fires PostToolUse for one resolved tool call. Spec §2.3.
func (hr *HookRunner) FirePostToolUse(ctx context.Context, threadID, turnID, model, toolName string, toolInput, toolResponse json.RawMessage, toolUseID string) hooks.PostToolUseAggregate {
	if hr == nil {
		return hooks.PostToolUseAggregate{}
	}
	in := postToolUseInput{
		CommonInput:  hr.common(threadID, turnID, model, string(hooks.EventPostToolUse)),
		ToolName:     toolName,
		ToolInput:    toolInput,
		ToolResponse: toolResponse,
		ToolUseID:    toolUseID,
	}
	return hooks.AggregatePostToolUse(hr.fire(ctx, hooks.EventPostToolUse, toolName, in, hooks.InterpretPostToolUse))
}

// FireSessionStart fires SessionStart once for a Manager.Run start. Spec
// §2.6 — matched against source (per §1.5's "SessionStart -> source").
func (hr *HookRunner) FireSessionStart(ctx context.Context, threadID, model, source string) hooks.ContextAggregate {
	if hr == nil {
		return hooks.ContextAggregate{}
	}
	in := sessionStartInput{
		CommonInput: hooks.CommonInput{
			SessionID:      threadID,
			Cwd:            hr.cwd,
			HookEventName:  string(hooks.EventSessionStart),
			Model:          model,
			PermissionMode: hr.reportedPermissionMode(),
		},
		Source: source,
	}
	return hooks.AggregateContext(hr.fire(ctx, hooks.EventSessionStart, source, in, hooks.InterpretSessionStart))
}

// FireUserPromptSubmit fires UserPromptSubmit for the user_message opening
// turnID. Spec §2.7 — matcher is ignored for this event (events.go's
// MatcherIgnored), so matchAgainst is irrelevant; "" is passed for clarity.
func (hr *HookRunner) FireUserPromptSubmit(ctx context.Context, threadID, turnID, model, prompt string) hooks.ContextAggregate {
	if hr == nil {
		return hooks.ContextAggregate{}
	}
	in := userPromptSubmitInput{
		CommonInput: hr.common(threadID, turnID, model, string(hooks.EventUserPromptSubmit)),
		Prompt:      prompt,
	}
	return hooks.AggregateContext(hr.fire(ctx, hooks.EventUserPromptSubmit, "", in, hooks.InterpretUserPromptSubmit))
}

// FireStop fires Stop at the end of a successfully-completed turn. Spec
// §2.9/2.10 — matcher ignored for Stop.
func (hr *HookRunner) FireStop(ctx context.Context, threadID, turnID, model, lastAssistantMessage string) hooks.StopAggregate {
	if hr == nil {
		return hooks.StopAggregate{}
	}
	var lam *string
	if lastAssistantMessage != "" {
		lam = &lastAssistantMessage
	}
	in := stopInput{
		CommonInput:          hr.common(threadID, turnID, model, string(hooks.EventStop)),
		StopHookActive:       false, // this engine never loops a Stop continuation yet — see fireStop's doc comment.
		LastAssistantMessage: lam,
	}
	return hooks.AggregateStop(hr.fire(ctx, hooks.EventStop, "", in, hooks.InterpretStop))
}
