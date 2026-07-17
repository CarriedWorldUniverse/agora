// TestFlowPlanGate (blueprint §3.4): plan submit raises the KindPlan gate;
// the model's own open_questions carry THEIR OWN author-assigned IDs
// (contracts.PlanArtifact.OpenQuestions, a `plan` tool argument, unlike
// question_park_resume's QuestionLog-minted id) — so, unlike that flow,
// this one CAN be driven byte-for-byte against the golden fixture: nothing
// here is randomly generated. Pipe mode (no daemon/session needed — the
// real seams driven are internal/planning.PlanLog/QuestionLog/Gate
// directly), fixed attribution constant matching pipe's one-implicit-client
// model (§3.2a's same convention).
package conformance

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
)

const planGateBy = "agora:q7ymdevice001"

var flowPlanArtifact = contracts.PlanArtifact{
	Phase: contracts.PhasePlan,
	Steps: []string{
		"extract parser into internal/parse",
		"table tests for the 6 grammars",
		"wire into cmd",
	},
	OpenQuestions: []contracts.QuestionAsked{{
		ID: "oq_0001", Source: contracts.QuestionFromAgent, Blocking: true,
		Args: contracts.QuestionArgs{
			Text:    "Keep the legacy grammar behind a flag, or drop it?",
			Options: []contracts.QuestionOption{{Label: "flag"}, {Label: "drop"}},
		},
	}},
	Submit: true,
}

// projectOutstandingOpenQuestions returns a COPY of plan with OpenQuestions
// filtered down to only the ids NOT present in answered — the
// outstanding-open-questions projection (blueprint §3.4/§4, REUSING
// contracts_test.go's TestPlanGateTeethAgainstFixtureMutation algorithm:
// seed a set from the submitted plan's open_questions ids, delete on each
// question.answered id, and the REMAINING set is what must still block the
// gate). An off-by-one here silently defeats invariant 6 (planning.Gate
// refuses an allow while len(OpenQuestions) > 0) — this is the riskiest
// point in this flow (blueprint §5.2).
func projectOutstandingOpenQuestions(plan contracts.PlanArtifact, answered map[string]bool) contracts.PlanArtifact {
	out := plan
	out.OpenQuestions = nil
	for _, oq := range plan.OpenQuestions {
		if !answered[oq.ID] {
			out.OpenQuestions = append(out.OpenQuestions, oq)
		}
	}
	return out
}

func driveFlowPlanGate(t *testing.T) []byte {
	t.Helper()
	store := persistence.NewMemStore()
	questions := planning.NewQuestionLog(store)
	plans := planning.NewPlanLog(store)
	threadID, turnID := "th_0004", "tu_0001"
	ts := time.Unix(0, 0).UTC()

	if err := store.Create(contracts.ThreadMeta{ThreadID: threadID, CreatedAt: ts, Profile: "dev"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := plans.Update(threadID, flowPlanArtifact, ts, planGateBy); err != nil {
		t.Fatalf("PlanLog.Update: %v", err)
	}

	answered := map[string]bool{}
	steps := []awaitStep{
		{Emit: []contracts.Event{
			newThreadStarted(threadID, threadStartedPayload{IdentityFP: "agora:k5xw3zjanfzsa2lt", Profile: "dev", WorkingDir: "/work/demo"}),
			newTurnStarted(threadID, turnID),
			{Type: contracts.EvItemStarted, ThreadID: threadID, TurnID: turnID, Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemPlan}},
			{Type: contracts.EvItemCompleted, ThreadID: threadID, TurnID: turnID, Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemPlan}, Payload: mustMarshalJSON(flowPlanArtifact)},
			{Type: contracts.EvApprovalRequested, ThreadID: threadID, TurnID: turnID, Payload: mustMarshalJSON(contracts.ApprovalRequest{
				ID: "ap_0001", Kind: contracts.KindPlan, Payload: flowPlanArtifact,
			})},
		}},
		{
			Await: contracts.InQuestionResponse, AwaitID: "oq_0001",
			Resolve: func(in contracts.Input) ([]contracts.Event, error) {
				ans := contracts.Answer{AnswerInput: *in.Answer, By: planGateBy}
				// REAL seam call — a legal no-op on the park side (question.go:
				// 191-194): nothing was ever parked on oq_0001 (it's plan-authored
				// content, never routed through QuestionLog.Ask), so this only
				// appends the durable TIQuestionAnswered audit item.
				if err := questions.Answer(threadID, "oq_0001", ans, ts, planGateBy); err != nil {
					return nil, err
				}
				answered["oq_0001"] = true
				return []contracts.Event{{
					Type: contracts.EvQuestionAnswered, ThreadID: threadID, TurnID: turnID,
					Payload: mustMarshalJSON(questionAnsweredWirePayload{ID: "oq_0001", Answer: ans}),
				}}, nil
			},
		},
		{
			Await: contracts.InApprovalResponse, AwaitID: "ap_0001",
			Resolve: func(in contracts.Input) ([]contracts.Event, error) {
				projected := projectOutstandingOpenQuestions(flowPlanArtifact, answered)
				outcome, err := planning.Gate(planning.GateRequest{
					Plan: projected, Decision: contracts.DecisionAllow, Exit: contracts.ExitInline, By: planGateBy,
				})
				if err != nil {
					return nil, err
				}
				// planning.Gate's ApprovalResolution deliberately carries neither
				// ID (correlation is the caller's job — every OTHER seam's
				// Resolution helper takes an id param; Gate's doesn't) nor Scope
				// (plan approvals aren't policy-scoped the way permission kinds
				// are) — the flow-engine, as the daemon layer would, fills both
				// in before putting the resolution on the wire.
				res := outcome.Resolution
				res.ID = "ap_0001"
				res.Scope = contracts.ScopeOnce
				return []contracts.Event{
					{Type: contracts.EvApprovalResolved, ThreadID: threadID, TurnID: turnID, Payload: mustMarshalJSON(res)},
					newTurnCompleted(threadID, turnID, contracts.Usage{Input: 5200, Output: 760}),
				}, nil
			},
		},
	}
	engine := &flowEngine{steps: steps}

	in := strings.NewReader(
		`{"type":"user_message","text":"decompose the parser rewrite into a plan"}` + "\n" +
			`{"type":"question_response","id":"oq_0001","answer":{"choice":["drop"]}}` + "\n" +
			`{"type":"approval_response","id":"ap_0001","decision":"allow"}` + "\n",
	)
	var out, errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := agoraio.RunPipe(ctx, in, &out, &errBuf, engine, agoraio.PipeOptions{})
	if err != nil {
		t.Fatalf("RunPipe: %v", err)
	}
	if code != agoraio.ExitCompleted {
		t.Fatalf("exit code = %d, want ExitCompleted, stderr=%s", code, errBuf.String())
	}
	return out.Bytes()
}

func TestFlowPlanGate(t *testing.T) {
	got := driveFlowPlanGate(t)
	want := rawFlow(t, "plan_gate.jsonl")
	if !bytes.Equal(got, want) {
		t.Fatalf("stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
