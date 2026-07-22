package turnengine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// This file brings planning/questions (docs/spec/agora-spec-planning-
// questions.md) to the live turn path: the `question` and `plan`
// harness-intrinsic tools (advertised by toolrunner.PlanningFamily,
// gated here in beforeToolCall — see approval.go's early dispatch to
// handleQuestionCall/handlePlanCall) plus the InQuestionResponse resume
// path (manager.go's Run loop).
//
// answeringIdentity is the placeholder answering/recording identity this
// layer stamps on question answers (contracts.Answer.By, required
// non-empty — planning.QuestionLog.Answer refuses an unattributed one)
// and thread-item Identity fields. Manager has no real per-connection
// identity plumbing yet (persistTurn's own ThreadItem construction never
// sets Identity either — see manager.go) — a future unit that threads the
// authenticated connection identity down from internal/io.Session should
// replace this constant at its two call sites below, not invent a new one.
const answeringIdentity = "operator"

// questionOutcome is what a pending blocking-question rendezvous
// (waitForQuestionAnswer) resolves to: either a real question_response
// Input (Cancelled false, Answer carried verbatim) or a cancellation
// marker (the turn was interrupted/torn down while the question was still
// parked). Mirrors approvalOutcome (approval.go) exactly, one layer over.
type questionOutcome struct {
	Cancelled bool
	Answer    contracts.AnswerInput
}

// registerQuestionWaiter/dropQuestionWaiter/resolveQuestionWaiter guard the
// question rendezvous registry (question id -> waiter channel) — the exact
// same cross-goroutine shape as registerWaiter/dropWaiter/resolveWaiter
// (approval.go), just keyed by the QuestionAsked.ID planning.QuestionLog.Ask
// mints (NOT the tool call id) and resolved by InQuestionResponse instead of
// InApprovalResponse.
func (m *Manager) registerQuestionWaiter(id string) chan questionOutcome {
	ch := make(chan questionOutcome, 1)
	m.questionWaiterMu.Lock()
	m.questionWaiters[id] = ch
	m.questionWaiterMu.Unlock()
	return ch
}

func (m *Manager) dropQuestionWaiter(id string) {
	m.questionWaiterMu.Lock()
	delete(m.questionWaiters, id)
	m.questionWaiterMu.Unlock()
}

// resolveQuestionWaiter is called from Manager.Run's InQuestionResponse
// case. A response whose id has no waiter (already resolved, a stray/
// duplicate/forged id, or a non-blocking question that was never parked in
// the first place) is silently ignored — same fail-safe posture as
// resolveWaiter (approval.go).
func (m *Manager) resolveQuestionWaiter(id string, out questionOutcome) {
	m.questionWaiterMu.Lock()
	ch, ok := m.questionWaiters[id]
	if ok {
		delete(m.questionWaiters, id)
	}
	m.questionWaiterMu.Unlock()
	if ok {
		ch <- out
	}
}

// denyWithResult builds the bridle.BeforeToolCallCtx that short-circuits a
// tool call via Deny+Result (skips Surface.Execute entirely — see
// toolrunner.PlanningFamily's doc comment): resultText on success (no
// Go/tool error at all — Deny+Result-with-no-Err is a NORMAL, successful
// tool_result, not a refusal, exactly like ctxmap's own recall/inspect
// hook uses it), errText on failure. Never both.
func denyWithResult(c bridle.BeforeToolCallCtx, resultText, errText string) (bridle.BeforeToolCallCtx, bridle.HookAction, error) {
	c.Deny = true
	if errText != "" {
		c.Err = errText
	} else {
		c.Result = mustMarshal(resultText)
	}
	return c, bridle.HookContinue, nil
}

// --- question ---

// questionCallArgs is the `question` tool call's raw args shape
// (contracts.QuestionArgs's own doc comment: "exactly and only what the
// model's harness-intrinsic `question` tool supplies").
type questionCallArgs struct {
	Payload  contracts.QuestionArgs `json:"payload"`
	Blocking bool                   `json:"blocking"`
}

// threadWaitingPayload/threadResumedPayload/questionAnsweredPayload are the
// wire shapes for EvThreadWaiting/EvThreadResumed/EvQuestionAnswered —
// byte-shape matched against contracts/testdata/flows/question_park_resume.jsonl.
type threadWaitingPayload struct {
	QuestionID string `json:"question_id"`
}

type threadResumedPayload struct {
	QuestionID string `json:"question_id"`
}

type questionAnsweredPayload struct {
	ID     string           `json:"id"`
	Answer contracts.Answer `json:"answer"`
}

// handleQuestionCall resolves one `question` tool call entirely inside
// beforeToolCall (see approval.go's early dispatch): mint+persist the
// question (planning.QuestionLog.Ask), emit question.asked, then route by
// disposition — DispositionQueue returns immediately (the turn continues);
// DispositionPark emits thread.waiting and blocks in waitForQuestionAnswer
// until an InQuestionResponse resolves it or the turn is torn down.
// die_honestly/bubble are unreachable here: this Manager only ever raises
// ContextInteractive (dispatch-pod/subagent contexts are a different
// engine entirely, out of this unit's scope — see doc.go).
// Spec: agora-spec-planning-questions.md §4/§5/§6, §7 (wire).
func (m *Manager) handleQuestionCall(turnCtx context.Context, htc *turnHookCtx, c bridle.BeforeToolCallCtx) (bridle.BeforeToolCallCtx, bridle.HookAction, error) {
	var a questionCallArgs
	if err := json.Unmarshal(c.Call.Args, &a); err != nil {
		return denyWithResult(c, "", "question: malformed arguments: "+err.Error())
	}
	if a.Payload.Text == "" {
		return denyWithResult(c, "", "question: payload.text is required")
	}

	out, err := m.questionLog.Ask(planning.AskRequest{
		ThreadID: m.threadID,
		Args:     a.Payload,
		Source:   contracts.QuestionFromAgent,
		Blocking: a.Blocking,
		Context:  planning.ContextInteractive,
		TS:       m.now(),
		Identity: answeringIdentity,
	})
	if err != nil {
		return denyWithResult(c, "", "question: "+err.Error())
	}

	if !m.emitPlanningEvent(turnCtx, htc, contracts.Event{
		Type:    contracts.EvQuestionAsked,
		Payload: mustMarshal(out.Question),
	}) {
		return c, bridle.HookAbort, nil
	}

	switch out.Disposition {
	case planning.DispositionQueue:
		return denyWithResult(c, "question filed (non-blocking); the thread continues", "")

	case planning.DispositionPark:
		if !m.emitPlanningEvent(turnCtx, htc, contracts.Event{
			Type:    contracts.EvThreadWaiting,
			Payload: mustMarshal(threadWaitingPayload{QuestionID: out.Question.ID}),
		}) {
			return c, bridle.HookAbort, nil
		}
		return m.waitForQuestionAnswer(turnCtx, htc, c, out.Question)

	default:
		// DispositionDieHonestly/DispositionBubble: not reachable from
		// ContextInteractive (planning.Resolve's own table) — defensive.
		return denyWithResult(c, "", fmt.Sprintf("question: disposition %q is not supported by the interactive turn engine", out.Disposition))
	}
}

// waitForQuestionAnswer blocks the turn goroutine (inside the
// BeforeToolCall hook, same as askAndWait — approval.go's doc comment on
// why that specific goroutine/ctx is what makes interrupt-abort correct)
// until either an InQuestionResponse resolves the registered waiter or
// turnCtx is torn down (interrupt/end). On answer: persists it
// (planning.QuestionLog.Answer — also un-parks the thread), emits
// question.answered + thread.resumed, and returns the answer as the tool's
// successful result (Deny+Result — the model's NEXT step sees it as an
// ordinary tool_result). On interrupt: returns HookAbort WITHOUT ever
// calling QuestionLog.Answer — the question stays parked/unanswered in the
// log, never fabricated (§6).
func (m *Manager) waitForQuestionAnswer(turnCtx context.Context, htc *turnHookCtx, c bridle.BeforeToolCallCtx, q contracts.QuestionAsked) (bridle.BeforeToolCallCtx, bridle.HookAction, error) {
	waiter := m.registerQuestionWaiter(q.ID)
	defer m.dropQuestionWaiter(q.ID)

	select {
	case res := <-waiter:
		if res.Cancelled {
			return c, bridle.HookAbort, nil
		}
		ts := m.now()
		ans := contracts.Answer{AnswerInput: res.Answer, By: answeringIdentity}
		if err := m.questionLog.Answer(m.threadID, q.ID, ans, ts, answeringIdentity); err != nil {
			return denyWithResult(c, "", "question: recording answer: "+err.Error())
		}

		if !m.emitPlanningEvent(turnCtx, htc, contracts.Event{
			Type:    contracts.EvQuestionAnswered,
			Payload: mustMarshal(questionAnsweredPayload{ID: q.ID, Answer: ans}),
		}) {
			return c, bridle.HookAbort, nil
		}
		if !m.emitPlanningEvent(turnCtx, htc, contracts.Event{
			Type:    contracts.EvThreadResumed,
			Payload: mustMarshal(threadResumedPayload{QuestionID: q.ID}),
		}) {
			return c, bridle.HookAbort, nil
		}

		// The model's next step sees the answer itself as the tool_result
		// (Deny+Result, no Err — a normal successful call, not a refusal).
		c.Deny = true
		c.Result = mustMarshal(res.Answer)
		return c, bridle.HookContinue, nil

	case <-turnCtx.Done():
		// The turn is being interrupted/torn down — same HookAbort
		// contract as askAndWait's identical case (approval.go): the
		// question remains parked, unanswered, in the log (§6).
		return c, bridle.HookAbort, nil
	}
}

// emitPlanningEvent stamps threadID/turnID and delivers ev on htc.out,
// racing htc.sendCtx.Done() (the outer Run-level ctx going away) and
// turnCtx.Done() (an interrupt) against the send so a full/undrained `out`
// can never wedge this goroutine past either. Unlike askAndWait's own
// two-select delivery (approval.go), which tolerates sendCtx dying by
// falling through to a SECOND select it knows will also catch turnCtx
// (turnCtx is a child context.WithCancel of sendCtx's ctx, so the latter
// dying always closes the former too), this single-shot helper treats
// EITHER ctx dying as "not delivered" — a safe over-approximation (the
// caller's contract is simply to stop and return HookAbort; there is no
// second select here that specifically needs to observe turnCtx itself).
func (m *Manager) emitPlanningEvent(turnCtx context.Context, htc *turnHookCtx, ev contracts.Event) bool {
	ev.ThreadID = htc.threadID
	ev.TurnID = htc.turnID
	select {
	case htc.out <- ev:
		return true
	case <-htc.sendCtx.Done():
		return false
	case <-turnCtx.Done():
		return false
	}
}

// --- plan ---

// handlePlanCall resolves one `plan` tool call: submit:false just persists
// the revision (planning.PlanLog.Update) — no gate, the plan object is
// always available (§1). submit:true raises the KindPlan approval through
// the SAME generic approval.Decide/askAndWait pipeline exec/patch/mcp_tool
// use (§3: "the plan gate ... approval pipeline" — the TUI's ResolvePlan
// already targets InApprovalResponse for KindPlan), and only persists the
// revision once that resolves to allow.
func (m *Manager) handlePlanCall(turnCtx context.Context, htc *turnHookCtx, c bridle.BeforeToolCallCtx) (bridle.BeforeToolCallCtx, bridle.HookAction, error) {
	var p contracts.PlanArtifact
	if err := json.Unmarshal(c.Call.Args, &p); err != nil {
		return denyWithResult(c, "", "plan: malformed arguments: "+err.Error())
	}

	if !p.Submit {
		return m.recordPlanRevision(c, p, "plan updated")
	}

	res := approval.Decide(m.policy, approval.Request{
		ID:        c.Call.ID,
		Kind:      contracts.KindPlan,
		SessionID: m.threadID,
	}, m.scopeStore)

	switch res.Action {
	case approval.ActionAllow:
		return m.recordPlanRevision(c, p, "plan approved")

	case approval.ActionDeny:
		return denyWithResult(c, "", "plan: "+res.Message)

	default: // approval.ActionAsk (defaultPolicy's PolicyPrompt) — escalate
		// via the exact same rendezvous exec/patch/mcp_tool use.
		newC, action, err := m.askAndWait(turnCtx, htc, c, contracts.KindPlan, "", p)
		if err != nil || action != bridle.HookContinue || newC.Deny {
			// Denied, aborted, or hook error: nothing to record — the
			// model sees the refusal/abort exactly as askAndWait built it.
			return newC, action, err
		}
		return m.recordPlanRevision(newC, p, "plan approved")
	}
}

// recordPlanRevision persists p as a new PlanLog revision and resolves the
// tool call successfully with resultText. The item.* event for this
// revision (ItemType plan) is NOT emitted here — it falls out of bridle's
// own ToolCallStart/ToolCallResult instrumentation automatically (sink.go's
// itemTypeForTool/emitToolStart/emitToolResult, extended for
// contracts.ToolPlan/contracts.ItemPlan), which fires for every tool call
// including a Deny+Result short-circuit (bridle run.go's executeToolCall
// Deny branch: `sink.Emit(ToolCallStart{...})` then `sink.Emit(tcr)`, both
// unconditional on Deny) — see sink.go's ItemPlan cases for the exact
// wire shape (matches contracts/testdata/flows/plan_gate.jsonl).
func (m *Manager) recordPlanRevision(c bridle.BeforeToolCallCtx, p contracts.PlanArtifact, resultText string) (bridle.BeforeToolCallCtx, bridle.HookAction, error) {
	if err := m.planLog.Update(m.threadID, p, m.now(), answeringIdentity); err != nil {
		return denyWithResult(c, "", "plan: "+err.Error())
	}
	return denyWithResult(c, resultText, "")
}
