package hooks

import "testing"

func outcome(seq, completion int, layer Layer, ho HandlerOutcome) Outcome {
	return Outcome{
		Handler:         ResolvedHandler{RegisteredHandler: RegisteredHandler{Seq: seq, Source: Source{Layer: layer}}},
		CompletionIndex: completion,
		HandlerOutcome:  ho,
	}
}

func TestAggregatePreToolUse_AnyBlockWinsFirstByDeclaration(t *testing.T) {
	outs := []Outcome{
		outcome(1, 1, LayerUser, HandlerOutcome{Continue: true}), // no block
		outcome(0, 0, LayerUser, HandlerOutcome{Continue: true, Block: true, Reason: "first declared block"}),
		outcome(2, 2, LayerUser, HandlerOutcome{Continue: true, Block: true, Reason: "second declared block"}),
	}
	got := AggregatePreToolUse(outs)
	if !got.Blocked {
		t.Fatal("expected Blocked")
	}
	if got.Reason != "first declared block" {
		t.Errorf("Reason = %q, want the FIRST block by declaration (Seq) order", got.Reason)
	}
}

func TestAggregatePreToolUse_UpdatedInputFromLastCompleted_BlockDropsIt(t *testing.T) {
	// No block: updatedInput picked from the last-COMPLETED handler, not
	// the last-declared one.
	outs := []Outcome{
		outcome(0, 1, LayerUser, HandlerOutcome{Continue: true, UpdatedInput: []byte(`{"v":"declared-first-completed-second"}`)}),
		outcome(1, 0, LayerUser, HandlerOutcome{Continue: true, UpdatedInput: []byte(`{"v":"declared-second-completed-first"}`)}),
	}
	got := AggregatePreToolUse(outs)
	if got.Blocked {
		t.Fatal("no block expected")
	}
	if string(got.UpdatedInput) != `{"v":"declared-first-completed-second"}` {
		t.Errorf("UpdatedInput = %s, want the LAST-COMPLETED handler's value", got.UpdatedInput)
	}

	// A block drops any updatedInput entirely.
	outsBlocked := append(outs, outcome(2, 2, LayerUser, HandlerOutcome{Continue: true, Block: true, Reason: "nope"}))
	gotBlocked := AggregatePreToolUse(outsBlocked)
	if !gotBlocked.Blocked || len(gotBlocked.UpdatedInput) != 0 {
		t.Errorf("a block must drop updatedInput entirely: Blocked=%v UpdatedInput=%s", gotBlocked.Blocked, gotBlocked.UpdatedInput)
	}
}

func TestAggregatePermissionRequest_AnyDenyWinsImmediately(t *testing.T) {
	outs := []Outcome{
		outcome(0, 0, LayerPlugin, HandlerOutcome{Continue: true, PRBehavior: "allow"}),
		outcome(1, 1, LayerUser, HandlerOutcome{Continue: true, PRBehavior: "deny", PRMessage: "blocked by user hook"}),
	}
	got := AggregatePermissionRequest(outs)
	if got.Decision != "deny" {
		t.Errorf("Decision = %q, want deny (any deny wins immediately, even from a lower-precedence layer)", got.Decision)
	}
	if got.Message != "blocked by user hook" {
		t.Errorf("Message = %q", got.Message)
	}
}

func TestAggregatePermissionRequest_HighestPrecedenceAllowWins(t *testing.T) {
	outs := []Outcome{
		outcome(0, 0, LayerUser, HandlerOutcome{Continue: true, PRBehavior: "allow", PRMessage: "user says ok"}),
		outcome(1, 1, LayerPlugin, HandlerOutcome{Continue: true, PRBehavior: "allow", PRMessage: "plugin says ok"}),
		outcome(2, 2, LayerProject, HandlerOutcome{Continue: true, PRBehavior: "allow", PRMessage: "project says ok"}),
	}
	got := AggregatePermissionRequest(outs)
	if got.Decision != "allow" || got.Message != "plugin says ok" {
		t.Errorf("got %+v, want allow from the highest-precedence layer (plugin)", got)
	}
}

func TestAggregatePermissionRequest_NoneFallsThrough(t *testing.T) {
	outs := []Outcome{
		outcome(0, 0, LayerUser, HandlerOutcome{Continue: true}),
		outcome(1, 1, LayerUser, HandlerOutcome{Continue: true}),
	}
	got := AggregatePermissionRequest(outs)
	if got.Decision != "" {
		t.Errorf("Decision = %q, want empty (fall through to normal approval flow)", got.Decision)
	}
}

func TestAggregatePostToolUse_FeedbackJoinedContextsFlattened(t *testing.T) {
	outs := []Outcome{
		outcome(0, 0, LayerUser, HandlerOutcome{Continue: true, Block: true, Reason: "first"}),
		outcome(1, 1, LayerUser, HandlerOutcome{Continue: true, Block: true, Reason: "second"}),
		outcome(2, 2, LayerUser, HandlerOutcome{Continue: true, AdditionalContext: "ctx-a"}),
		outcome(3, 3, LayerUser, HandlerOutcome{Continue: true, AdditionalContext: "ctx-b"}),
	}
	got := AggregatePostToolUse(outs)
	if !got.Blocked {
		t.Fatal("expected Blocked")
	}
	if got.Feedback != "first\n\nsecond" {
		t.Errorf("Feedback = %q, want feedback joined with \\n\\n in declaration order", got.Feedback)
	}
	if got.AdditionalContext != "ctx-a\n\nctx-b" {
		t.Errorf("AdditionalContext = %q, want contexts flattened", got.AdditionalContext)
	}
}

func TestAggregateStop_AnyStopWinsElseBlockJoinedInDeclarationOrder(t *testing.T) {
	// any stop wins.
	outs := []Outcome{
		outcome(0, 0, LayerUser, HandlerOutcome{Continue: true, Block: true, Reason: "keep going"}),
		outcome(1, 1, LayerUser, HandlerOutcome{Continue: false}), // stop
	}
	got := AggregateStop(outs)
	if !got.Stopped {
		t.Fatal("expected Stopped to win over a block")
	}

	// else any block, reasons joined \n\n, declaration order.
	outsNoStop := []Outcome{
		outcome(1, 0, LayerUser, HandlerOutcome{Continue: true, Block: true, Reason: "second declared"}),
		outcome(0, 1, LayerUser, HandlerOutcome{Continue: true, Block: true, Reason: "first declared"}),
	}
	gotLooped := AggregateStop(outsNoStop)
	if gotLooped.Stopped {
		t.Fatal("no stop expected")
	}
	if !gotLooped.Looped {
		t.Fatal("expected Looped (continuation)")
	}
	if gotLooped.Continuation != "first declared\n\nsecond declared" {
		t.Errorf("Continuation = %q, want fragments concatenated in DECLARATION (Seq) order", gotLooped.Continuation)
	}
}

func TestAggregateStop_NoStopNoBlock(t *testing.T) {
	got := AggregateStop([]Outcome{outcome(0, 0, LayerUser, HandlerOutcome{Continue: true})})
	if got.Stopped || got.Looped {
		t.Errorf("expected neither Stopped nor Looped, got %+v", got)
	}
}

func TestAggregateCompact_AnyContinueFalseHalts(t *testing.T) {
	outs := []Outcome{
		outcome(0, 0, LayerUser, HandlerOutcome{Continue: true}),
		outcome(1, 1, LayerUser, HandlerOutcome{Continue: false}),
	}
	if !AggregateCompact(outs).Halted {
		t.Error("expected Halted")
	}
	if AggregateCompact([]Outcome{outcome(0, 0, LayerUser, HandlerOutcome{Continue: true})}).Halted {
		t.Error("expected not Halted")
	}
}
