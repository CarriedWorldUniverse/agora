package bus

import (
	"testing"

	"github.com/CarriedWorldUniverse/nexus/nexus/frames"
)

// The load-bearing wire contract (verified against nexus
// broker/escalation.go): the escalation.decision frame the operator
// sends must carry the correlation id in the PAYLOAD field RequestID and
// MUST leave the envelope InReplyTo empty. A frame with envelope
// InReplyTo set would be routed through the broker's routeResponse
// (pending map) BEFORE the escalation handler runs and dropped — there
// is no broker-side pending entry. The broker reads payload.RequestID
// and stamps InReplyTo only on the frame it forwards to the aspect.
func TestBuildEscalationDecision_RequestIDInPayloadNotEnvelope(t *testing.T) {
	env, err := BuildEscalationDecision("anvil", frames.EscalationApprove, "operator", "looks fine", "01CORR")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if env.Kind != frames.KindEscalationDecision {
		t.Fatalf("kind: want %q got %q", frames.KindEscalationDecision, env.Kind)
	}

	// CRITICAL: envelope InReplyTo must be empty (do NOT use NewResponse).
	if env.InReplyTo != "" {
		t.Fatalf("env.InReplyTo must be empty to avoid broker routeResponse drop; got %q", env.InReplyTo)
	}

	var payload frames.EscalationDecisionPayload
	if err := frames.PayloadAs(env, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	// CRITICAL: correlation id lives in the payload.
	if payload.RequestID != "01CORR" {
		t.Fatalf("payload.RequestID: want %q got %q", "01CORR", payload.RequestID)
	}
	if payload.Aspect != "anvil" {
		t.Fatalf("payload.Aspect: want anvil got %q", payload.Aspect)
	}
	if payload.Decision != frames.EscalationApprove {
		t.Fatalf("payload.Decision: want %q got %q", frames.EscalationApprove, payload.Decision)
	}
	if payload.Operator != "operator" {
		t.Fatalf("payload.Operator: want operator got %q", payload.Operator)
	}
	if payload.Note != "looks fine" {
		t.Fatalf("payload.Note: want %q got %q", "looks fine", payload.Note)
	}
}

// Deny decisions use the frames.EscalationDeny constant and keep the
// same wire shape.
func TestBuildEscalationDecision_Deny(t *testing.T) {
	env, err := BuildEscalationDecision("wren", frames.EscalationDeny, "operator", "", "r-2")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if env.InReplyTo != "" {
		t.Fatalf("env.InReplyTo must be empty; got %q", env.InReplyTo)
	}
	var payload frames.EscalationDecisionPayload
	if err := frames.PayloadAs(env, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Decision != frames.EscalationDeny {
		t.Fatalf("payload.Decision: want %q got %q", frames.EscalationDeny, payload.Decision)
	}
	if payload.RequestID != "r-2" {
		t.Fatalf("payload.RequestID: want r-2 got %q", payload.RequestID)
	}
}
