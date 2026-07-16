package contracts

import "testing"

// TestBuiltinPresetMatrix transcribes the approvals-§2 preset table
// INDEPENDENTLY from the spec markdown (columns exec|patch|escalation|
// mcp_tool|question|plan — NO gate column) and asserts the code matches. The
// value is that this map is read off the spec table, not copied from the
// implementation; a drift between the two surfaces one of them.
func TestBuiltinPresetMatrix(t *testing.T) {
	// Rows are exactly the §2 table cells; "auto*" and "auto-within-sandbox"
	// both encode as PolicyAuto at the policy layer (the sandbox distinction
	// is the envelope's, not the policy value's).
	want := map[string]map[ApprovalKind]PolicyValue{
		PresetPrompt: {
			KindExec: PolicyPrompt, KindPatch: PolicyAuto, KindEscalation: PolicyPrompt,
			KindMCPTool: PolicyPerServer, KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt,
		},
		PresetAutoSafe: {
			KindExec: PolicyAuto, KindPatch: PolicyAuto, KindEscalation: PolicyPrompt,
			KindMCPTool: PolicyPerServer, KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt,
		},
		PresetStrict: {
			KindExec: PolicyPrompt, KindPatch: PolicyPrompt, KindEscalation: PolicyPrompt,
			KindMCPTool: PolicyPrompt, KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt,
		},
		PresetNeverEscalate: {
			KindExec: PolicyAuto, KindPatch: PolicyAuto, KindEscalation: PolicyDeny,
			KindMCPTool: PolicyPerServer, KindQuestion: PolicyConvert, KindPlan: PolicyDeny,
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
		if len(ps) != len(kinds) {
			t.Errorf("%s: kind count got %d want %d (a fabricated or missing column)", preset, len(ps), len(kinds))
		}
		for kind, v := range kinds {
			if ps[kind] != v {
				t.Errorf("%s/%s: got %q want %q", preset, kind, ps[kind], v)
			}
		}
		// No gate column exists in the spec table.
		if _, hasGate := ps[KindGate]; hasGate {
			t.Errorf("%s: preset contains KindGate — §2 has no gate column (gate always surfaces to the operator)", preset)
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

// TestPerServerOnlyForMCPTool: PolicyPerServer is valid ONLY on KindMCPTool —
// it means "defer to the server's approval_mode" and is meaningless elsewhere
// (approvals §2 mcp_tool column, agora-spec-mcp §1).
func TestPerServerOnlyForMCPTool(t *testing.T) {
	for preset, ps := range BuiltinPresets() {
		for kind, v := range ps {
			if v == PolicyPerServer && kind != KindMCPTool {
				t.Errorf("%s: PolicyPerServer on %q — only mcp_tool may defer per-server", preset, kind)
			}
		}
	}
}

// TestCapabilityMapping: the compiled capability map matches the spec — a
// question answer is interactive (not approver), config/provision are admin,
// and approver does not implicitly grant interactive (remote §4).
func TestCapabilityMapping(t *testing.T) {
	if RequiredForInput(InQuestionResponse) != CapInteractive {
		t.Error("question_response must require interactive, not approver")
	}
	if RequiredForInput(InConfig) != CapAdmin || RequiredForInput(InProvision) != CapAdmin {
		t.Error("config and provision must require admin")
	}
	if RequiredForApproval(KindQuestion) != CapInteractive {
		t.Error("answering a question kind must require interactive")
	}
	if RequiredForApproval(KindPlan) != CapApprover || RequiredForApproval(KindExec) != CapApprover {
		t.Error("plan/exec approvals must require approver")
	}
	if Holds([]Capability{CapApprover}, CapInteractive) {
		t.Error("approver must not implicitly satisfy interactive — tiers do not nest")
	}
	if !Holds([]Capability{CapApprover, CapInteractive}, CapInteractive) {
		t.Error("explicit interactive grant must satisfy interactive")
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
