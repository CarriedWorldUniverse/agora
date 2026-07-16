package approval

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestAuditLineDeterministic: the same Result marshaled twice produces
// byte-identical output — no map iteration in the serialized shape.
func TestAuditLineDeterministic(t *testing.T) {
	r := Result{
		Action:  ActionAllow,
		Kind:    contracts.KindExec,
		Scope:   contracts.ScopeSession,
		Stage:   contracts.StagePolicy,
		By:      "preset:auto-safe",
		Message: "",
	}
	a := NewAuditLine("req-1", r)

	b1, err := a.MarshalJSONLine()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b2, err := a.MarshalJSONLine()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if b1 != b2 {
		t.Fatalf("audit line not deterministic:\n%s\n%s", b1, b2)
	}

	want := `{"request_id":"req-1","kind":"exec","action":"allow","scope":"session","stage":"policy","by":"preset:auto-safe"}`
	if b1 != want {
		t.Errorf("audit line shape:\ngot  %s\nwant %s", b1, want)
	}
}

// TestAuditLineRecordsEveryOutcome: every decision (allow, deny, ask,
// convert) produces a well-formed audit line with stage+actor — invariant
// 3 (§4): "every decision is recorded with its stage + actor".
func TestAuditLineRecordsEveryOutcome(t *testing.T) {
	for _, r := range []Result{
		{Action: ActionAllow, Kind: contracts.KindExec, Stage: contracts.StagePolicy, By: "policy"},
		{Action: ActionDeny, Kind: contracts.KindEscalation, Stage: contracts.StagePolicy, By: "policy", Message: "denied by policy"},
		{Action: ActionAsk, Kind: contracts.KindPlan, Stage: contracts.StagePolicy, By: "policy"},
		{Action: ActionConvert, Kind: contracts.KindQuestion, Stage: contracts.StagePolicy, By: "policy"},
	} {
		a := NewAuditLine("req-x", r)
		if a.By == "" {
			t.Errorf("%s: audit line missing actor (By)", r.Action)
		}
		if a.Stage == "" {
			t.Errorf("%s: audit line missing stage", r.Action)
		}
		line, err := a.MarshalJSONLine()
		if err != nil {
			t.Fatalf("%s: marshal: %v", r.Action, err)
		}
		if line == "" {
			t.Errorf("%s: empty audit line", r.Action)
		}
	}
}

// TestResultResolutionOnlyForFinalDecisions: Result.Resolution converts to
// a contracts.ApprovalResolution only for allow/deny (the two decisions
// contracts.Decision can represent); ask/convert have no resolution yet
// (the call has not reached a final permission/plan decision, or resolves
// via the question/answer path instead).
func TestResultResolutionOnlyForFinalDecisions(t *testing.T) {
	allow := Result{Action: ActionAllow, Kind: contracts.KindExec, Scope: contracts.ScopeOnce, Stage: contracts.StagePolicy, By: "policy"}
	res, ok := allow.Resolution("id-1")
	if !ok {
		t.Fatal("allow must produce a resolution")
	}
	if res.Decision != contracts.DecisionAllow || res.ID != "id-1" {
		t.Errorf("unexpected resolution: %+v", res)
	}

	deny := Result{Action: ActionDeny, Kind: contracts.KindEscalation, Stage: contracts.StagePolicy, By: "policy", Message: "no"}
	res, ok = deny.Resolution("id-2")
	if !ok || res.Decision != contracts.DecisionDeny || res.Message != "no" {
		t.Errorf("unexpected deny resolution: %+v ok=%v", res, ok)
	}

	for _, r := range []Result{
		{Action: ActionAsk, Kind: contracts.KindPlan},
		{Action: ActionConvert, Kind: contracts.KindQuestion},
	} {
		if _, ok := r.Resolution("id-3"); ok {
			t.Errorf("%s must not produce a contracts.ApprovalResolution", r.Action)
		}
	}
}
