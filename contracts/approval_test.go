package contracts

import "testing"

// TestBuiltinPresetMatrix is the approvals-§2 preset table, verbatim, as a
// test. If the table and the code disagree, one of them is wrong — loudly.
func TestBuiltinPresetMatrix(t *testing.T) {
	want := map[string]map[ApprovalKind]PolicyValue{
		PresetPrompt: {
			KindExec: PolicyPrompt, KindPatch: PolicyAuto, KindEscalation: PolicyPrompt,
			KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt, KindGate: PolicyPrompt,
		},
		PresetAutoSafe: {
			KindExec: PolicyAuto, KindPatch: PolicyAuto, KindEscalation: PolicyPrompt,
			KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt, KindGate: PolicyPrompt,
		},
		PresetStrict: {
			KindExec: PolicyPrompt, KindPatch: PolicyPrompt, KindEscalation: PolicyPrompt,
			KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt, KindGate: PolicyPrompt,
		},
		PresetNeverEscalate: {
			KindExec: PolicyAuto, KindPatch: PolicyAuto, KindEscalation: PolicyDeny,
			KindQuestion: PolicyConvert, KindPlan: PolicyDeny, KindGate: PolicyDeny,
		},
	}
	got := BuiltinPresets()
	if len(got) != len(want) {
		t.Fatalf("preset count: got %d want %d", len(got), len(want))
	}
	for preset, kinds := range want {
		ps, ok := got[preset]
		if !ok {
			t.Fatalf("missing preset %q", preset)
		}
		for kind, v := range kinds {
			if ps[kind] != v {
				t.Errorf("%s/%s: got %q want %q", preset, kind, ps[kind], v)
			}
		}
	}
}

// TestConvertOnlyForQuestions: PolicyConvert is valid ONLY on KindQuestion —
// questions are never auto-answered and never silently denied; every other
// kind must resolve auto/prompt/deny (approvals §2 footnote †).
func TestConvertOnlyForQuestions(t *testing.T) {
	for preset, ps := range BuiltinPresets() {
		for kind, v := range ps {
			if v == PolicyConvert && kind != KindQuestion {
				t.Errorf("%s: PolicyConvert on %q — only question may convert", preset, kind)
			}
		}
	}
}

// TestNeverEscalateNeverBlocksOnHuman: the headless preset must contain no
// Prompt value anywhere — it never blocks on a human (approvals §3: pipe
// --approval-policy auto maps here, "never blocks on a human").
func TestNeverEscalateNeverBlocksOnHuman(t *testing.T) {
	for kind, v := range BuiltinPresets()[PresetNeverEscalate] {
		if v == PolicyPrompt {
			t.Errorf("never-escalate prompts on %q", kind)
		}
	}
}

// TestQuestionNeverPolicyDeniedInBuiltins: no built-in preset flat-denies
// questions — missing information is parked/converted or asked, never
// silently refused (planning-questions §6 invariant 1).
func TestQuestionNeverPolicyDeniedInBuiltins(t *testing.T) {
	for preset, ps := range BuiltinPresets() {
		if ps[KindQuestion] == PolicyDeny {
			t.Errorf("%s: question policy is deny — must be prompt or convert", preset)
		}
	}
}
