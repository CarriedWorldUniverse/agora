package pod

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
)

// TestRunTurn_RefusedWhileBlank: "boots blank ... refuses turns until
// provisioned" (§6a) — a fresh, unprovisioned Pod must refuse RunTurn.
func TestRunTurn_RefusedWhileBlank(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	_, err := p.RunTurn(ctx, "do the thing")
	if !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("RunTurn on blank pod error = %v, want ErrNotProvisioned", err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestRunTurn_CompletesNormally_NoQuestion: the non-blocked path — the
// engine runs a turn to completion and RunTurn reports it with Blocked nil,
// proving the harness conversion doesn't fire on an ordinary turn.
func TestRunTurn_CompletesNormally_NoQuestion(t *testing.T) {
	ctx := context.Background()
	engine := &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{
		{Events: []contracts.Event{
			{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemAgentMessage}},
			{Type: contracts.EvTurnCompleted, Payload: mustJSON(t, contracts.Usage{Input: 10, Output: 5})},
		}},
	}}
	p, _ := newTestPod(t, ctx, engine)
	device := dispatchDevice("disp-turn", remote.DeviceConstraints{})
	if _, err := p.Provision(ctx, device, validNewProvision("aspect-builder")); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	result, err := p.RunTurn(ctx, "ship the feature")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if result.Blocked != nil {
		t.Fatalf("Blocked = %+v, want nil for a turn with no question", result.Blocked)
	}
	var sawCompleted bool
	for _, ev := range result.Events {
		if ev.Type == contracts.EvTurnCompleted {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Errorf("result.Events missing turn.completed: %+v", result.Events)
	}
}

// TestRunTurn_BlockingQuestion_ConvertsToBlockedNeedsInput is the core U17
// behavior: a blocking question mid-turn in a dispatch pod is NEVER parked
// (there is no interactive human) — it dies honestly. RunTurn must return
// TurnResult.Blocked, never forward the model past the unanswered question,
// and never fabricate an answer.
func TestRunTurn_BlockingQuestion_ConvertsToBlockedNeedsInput(t *testing.T) {
	ctx := context.Background()
	q := contracts.QuestionArgs{Text: "which registry should the pod publish to?", FreeText: true}
	engine := &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{
		{Events: []contracts.Event{
			{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemAgentMessage}},
			{Type: contracts.EvQuestionAsked, Payload: mustJSON(t, contracts.QuestionAsked{
				ID: "q_wire_placeholder", Source: contracts.QuestionFromAgent, Blocking: true, Args: q,
			})},
			// Nothing after this: a real engine halts here too — die-honestly
			// means the turn does not continue past an unanswered blocking
			// question. If RunTurn ever read further, this would panic on an
			// unscripted turn continuation, which is exactly the failure mode
			// this test wants to catch.
		}},
	}}
	p, store := newTestPod(t, ctx, engine)
	device := dispatchDevice("disp-block", remote.DeviceConstraints{})
	info, err := p.Provision(ctx, device, validNewProvision("aspect-builder"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	result, err := p.RunTurn(ctx, "publish the release")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if result.Blocked == nil {
		t.Fatalf("Blocked = nil, want a BlockedNeedsInput — a blocking question in a dispatch pod must die honestly, not silently complete")
	}
	if result.Blocked.Question.Args.Text != q.Text {
		t.Errorf("Blocked.Question.Args.Text = %q, want %q", result.Blocked.Question.Args.Text, q.Text)
	}
	if !result.Blocked.Question.Blocking {
		t.Errorf("Blocked.Question.Blocking = false, want true")
	}
	if result.Blocked.ThreadID != info.ThreadID {
		t.Errorf("Blocked.ThreadID = %q, want %q", result.Blocked.ThreadID, info.ThreadID)
	}
	// The harness mints its own question ID (never trusts the model's own
	// claimed id) — the outgoing id must NOT be the placeholder literal the
	// script above carried on the wire event, and must be non-empty.
	if result.Blocked.Question.ID == "" || result.Blocked.Question.ID == "q_wire_placeholder" {
		t.Errorf("Blocked.Question.ID = %q, want a freshly-minted, non-placeholder id", result.Blocked.Question.ID)
	}

	// Never-fabricate + audit trail: the question is durably recorded on the
	// thread even though the pod terminated the turn (planning-questions §6
	// invariant 2/4 — durable, visible; §5's dispatch row is "die honestly",
	// not "forget").
	items := threadItems(t, store, info.ThreadID)
	var sawQuestion bool
	for _, it := range items {
		if it.Type == contracts.TIQuestionAsked {
			sawQuestion = true
		}
		// A dispatch pod's blocking question must NEVER produce a TIParked
		// item — parking is the interactive-thread disposition (§5), not the
		// dispatch-pod one. This is the "never a live wire into a sleeping
		// process" invariant made concrete.
		if it.Type == contracts.TIParked {
			t.Errorf("thread has a TIParked item — a dispatch pod's blocking question must die honestly, never park: %+v", it)
		}
	}
	if !sawQuestion {
		t.Errorf("no TIQuestionAsked audit item found in thread %s: %+v", info.ThreadID, items)
	}
}
