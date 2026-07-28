package main

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestSubagentProfile_NeverAsks pins the invariant behind agora#152: a
// subagent thread has NO attached approver, so any policy that can resolve
// to ASK parks the child — and, because agent() spawns Foreground and waits
// on Result with no timeout, parks the PARENT's turn with it.
//
// The live incident: a child pointed outside the working dir classified
// KindEscalation, blocked on a rendezvous with no counterparty, and produced
// nothing for 30+ minutes. Its whole transcript was one preamble message and
// zero tool calls.
//
// This asserts the property (no kind may ask) rather than the preset name,
// so swapping presets later cannot silently reintroduce the hang.
func TestSubagentProfile_NeverAsks(t *testing.T) {
	policy := subagentProfile().Policy
	if len(policy) == 0 {
		t.Fatal("subagent profile has an empty policy — it would fall back to a prompting default")
	}
	for kind, p := range policy {
		if p == contracts.PolicyPrompt {
			t.Errorf("kind %q is PolicyPrompt — a subagent has no approver to answer it, so this parks the child and its parent forever (agora#152)", kind)
		}
	}
	// Escalation specifically is the one that bit: anything naming a path
	// outside the working dir classifies KindEscalation.
	if got := policy[contracts.KindEscalation]; got == contracts.PolicyPrompt {
		t.Errorf("KindEscalation = %v; want a non-asking policy", got)
	}
}

// TestSubagentProfile_InheritsTheLaneOtherwise guards against the fix
// drifting into "subagents get a totally different profile": only the policy
// should differ from the interactive lane.
func TestSubagentProfile_InheritsTheLaneOtherwise(t *testing.T) {
	sub := subagentProfile()
	if sub.Model == "" {
		t.Error("subagent profile has no model")
	}
	if sub.AppendSystemPrompt == "" {
		t.Error("subagent profile has no system prompt — children should share the lane's prompt")
	}
}
