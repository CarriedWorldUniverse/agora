package approval

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestPresetKindMatrix is the DoD's "preset × kind matrix": every built-in
// preset against every kind (including KindGate, which has no preset
// column — §2 note — and always asks), transcribed independently from the
// §2 table plus the gate/convert/per-server resolution rules in §2's
// footnote and invariants §4. This is the table-driven core of the
// decision pipeline's observable behavior.
func TestPresetKindMatrix(t *testing.T) {
	kinds := []contracts.ApprovalKind{
		contracts.KindExec, contracts.KindPatch, contracts.KindEscalation,
		contracts.KindMCPTool, contracts.KindQuestion, contracts.KindPlan,
		contracts.KindGate,
	}

	want := map[string]map[contracts.ApprovalKind]Action{
		contracts.PresetPrompt: {
			contracts.KindExec: ActionAsk, contracts.KindPatch: ActionAllow,
			contracts.KindEscalation: ActionAsk, contracts.KindMCPTool: ActionAsk,
			contracts.KindQuestion: ActionAsk, contracts.KindPlan: ActionAsk,
			contracts.KindGate: ActionAsk,
		},
		contracts.PresetAutoSafe: {
			contracts.KindExec: ActionAllow, contracts.KindPatch: ActionAllow,
			contracts.KindEscalation: ActionAsk, contracts.KindMCPTool: ActionAsk,
			contracts.KindQuestion: ActionAsk, contracts.KindPlan: ActionAsk,
			contracts.KindGate: ActionAsk,
		},
		contracts.PresetStrict: {
			contracts.KindExec: ActionAsk, contracts.KindPatch: ActionAsk,
			contracts.KindEscalation: ActionAsk, contracts.KindMCPTool: ActionAsk,
			contracts.KindQuestion: ActionAsk, contracts.KindPlan: ActionAsk,
			contracts.KindGate: ActionAsk,
		},
		contracts.PresetNeverEscalate: {
			contracts.KindExec: ActionAllow, contracts.KindPatch: ActionAllow,
			contracts.KindEscalation: ActionDeny, contracts.KindMCPTool: ActionAsk,
			// The convert† exception: never-escalate never blocks on a human,
			// and question is never denied — it converts.
			contracts.KindQuestion: ActionConvert, contracts.KindPlan: ActionDeny,
			contracts.KindGate: ActionAsk,
		},
	}

	presets := contracts.BuiltinPresets()
	for presetName, byKind := range want {
		ps, ok := presets[presetName]
		if !ok {
			t.Fatalf("preset %q not found in contracts.BuiltinPresets", presetName)
		}
		for _, kind := range kinds {
			wantAction := byKind[kind]
			req := Request{ID: "req1", Kind: kind, SessionID: "th1"}
			got := Decide(ps, req, nil)
			if got.Action != wantAction {
				t.Errorf("%s/%s: got %q want %q", presetName, kind, got.Action, wantAction)
			}
		}
	}
}

// TestConvertExceptionExplicit isolates the question convert† exception:
// under never-escalate, a question request converts — never asks (it would
// block on a human, violating "never blocks"), never denies (never
// deny-fabricates, invariant 2).
func TestConvertExceptionExplicit(t *testing.T) {
	ps := contracts.BuiltinPresets()[contracts.PresetNeverEscalate]
	got := Decide(ps, Request{ID: "q1", Kind: contracts.KindQuestion, SessionID: "th1"}, nil)
	if got.Action != ActionConvert {
		t.Fatalf("never-escalate question: got %q want %q", got.Action, ActionConvert)
	}
	if got.Action == ActionDeny || got.Action == ActionAsk {
		t.Fatalf("never-escalate question must not deny or ask (blocks on a human)")
	}
}

// TestUnknownKindFailsClosed: an unrecognized ApprovalKind must never allow —
// it is treated as the most dangerous possible request.
func TestUnknownKindFailsClosed(t *testing.T) {
	ps := contracts.BuiltinPresets()[contracts.PresetAutoSafe]
	got := Decide(ps, Request{ID: "u1", Kind: contracts.ApprovalKind("made_up"), SessionID: "th1"}, nil)
	if got.Action != ActionDeny {
		t.Fatalf("unknown kind: got %q want %q (fail closed)", got.Action, ActionDeny)
	}
}

// TestMissingPolicyEntryFailsClosed: a policy set that omits a known kind
// (a malformed/incomplete custom preset) must never silently allow.
func TestMissingPolicyEntryFailsClosed(t *testing.T) {
	ps := contracts.PolicySet{} // nothing configured
	got := Decide(ps, Request{ID: "m1", Kind: contracts.KindExec, SessionID: "th1"}, nil)
	if got.Action == ActionAllow {
		t.Fatalf("missing policy entry must not allow, got %q", got.Action)
	}
}

// TestMalformedPolicyValueFailsClosed: a garbage PolicyValue string must
// never resolve to allow.
func TestMalformedPolicyValueFailsClosed(t *testing.T) {
	ps := contracts.PolicySet{contracts.KindExec: contracts.PolicyValue("bogus")}
	got := Decide(ps, Request{ID: "b1", Kind: contracts.KindExec, SessionID: "th1"}, nil)
	if got.Action == ActionAllow {
		t.Fatalf("malformed policy value must not allow, got %q", got.Action)
	}
}

// TestQuestionNeverActuallyDenied: even a misconfigured custom policy set
// that sets KindQuestion=deny must not produce ActionDeny — the engine
// enforces "never deny-fabricates" (invariant 2) as a property of Decide
// itself, not just of the shipped presets.
func TestQuestionNeverActuallyDenied(t *testing.T) {
	ps := contracts.PolicySet{contracts.KindQuestion: contracts.PolicyDeny}
	got := Decide(ps, Request{ID: "q2", Kind: contracts.KindQuestion, SessionID: "th1"}, nil)
	if got.Action == ActionDeny {
		t.Fatalf("question must never resolve to deny, got %q", got.Action)
	}
}

// TestConvertOnlyAppliesToQuestion: PolicyConvert set on a non-question kind
// (misconfiguration) must not be honored as Convert or Allow — fail closed.
func TestConvertOnlyAppliesToQuestion(t *testing.T) {
	ps := contracts.PolicySet{contracts.KindExec: contracts.PolicyConvert}
	got := Decide(ps, Request{ID: "c1", Kind: contracts.KindExec, SessionID: "th1"}, nil)
	if got.Action == ActionAllow || got.Action == ActionConvert {
		t.Fatalf("convert on non-question kind must fail closed, got %q", got.Action)
	}
}

// TestPerServerOnlyAppliesToMCPTool: PolicyPerServer on a non-mcp_tool kind
// must not be honored — fail closed, never allow.
func TestPerServerOnlyAppliesToMCPTool(t *testing.T) {
	ps := contracts.PolicySet{contracts.KindExec: contracts.PolicyPerServer}
	got := Decide(ps, Request{ID: "p1", Kind: contracts.KindExec, SessionID: "th1"}, nil)
	if got.Action == ActionAllow {
		t.Fatalf("per-server on non-mcp_tool kind must not allow, got %q", got.Action)
	}
}

// TestPolicyDenyIsFinal: invariant 1 — a policy deny is final for that call
// regardless of any scope store contents (a scope allow cannot have been
// legitimately granted under a hard-deny policy, but the engine must not
// let a stray scope entry override a deny either).
func TestPolicyDenyIsFinal(t *testing.T) {
	store := NewMemScopeStore()
	// Force an entry into the store directly to simulate a stray/corrupt
	// scope record, and confirm deny still wins.
	if err := store.Grant(ScopeAllow{Kind: contracts.KindEscalation, Scope: contracts.ScopeSession, Key: "th1", By: "test"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	ps := contracts.PolicySet{contracts.KindEscalation: contracts.PolicyDeny}
	got := Decide(ps, Request{ID: "d1", Kind: contracts.KindEscalation, SessionID: "th1"}, store)
	if got.Action != ActionDeny {
		t.Fatalf("policy deny must be final even with a scope entry present, got %q", got.Action)
	}
}
