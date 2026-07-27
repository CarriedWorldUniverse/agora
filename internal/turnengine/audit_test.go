package turnengine

import (
	"encoding/json"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// NEX-825: spec §4 invariant 3 requires every approval decision to be
// recorded with its stage and actor. approval.NewAuditLine existed but had
// ZERO call sites, so nothing durable said who allowed what — this pins that
// a decision now lands on the thread as a structured item.
func TestManager_ApprovalDecisionIsAudited(t *testing.T) {
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_audit"}); err != nil {
		t.Fatal(err)
	}
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "out.txt", "hello")}},
		fake.Step{Text: "done"},
	)
	policy := defaultPolicy()
	policy[contracts.KindPatch] = contracts.PolicyAuto
	_, in, out, runErr := newTestManagerWithStore(t, "th_audit", store, provider,
		WithRoots(managerTestRoots(t)), WithPolicy(policy))

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write out.txt"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	endAndClose(t, in, out, runErr)

	it, err := store.Resume("th_audit")
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	var audits []contracts.ThreadItem
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type == contracts.TIApprovalDecision {
			audits = append(audits, item)
		}
	}
	if len(audits) != 1 {
		t.Fatalf("got %d approval_decision items; want 1 (the auto-allowed write)", len(audits))
	}

	var line struct {
		RequestID string `json:"request_id"`
		Kind      string `json:"kind"`
		Action    string `json:"action"`
		Stage     string `json:"stage"`
		By        string `json:"by"`
	}
	b, err := json.Marshal(audits[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &line); err != nil {
		t.Fatalf("audit payload is not an approval.AuditLine: %v (%s)", err, b)
	}
	if line.Kind != string(contracts.KindPatch) || line.Action != "allow" {
		t.Errorf("audit line = %+v; want kind=patch action=allow", line)
	}
	if line.Stage == "" || line.By == "" {
		t.Errorf("invariant 3 requires stage AND actor; got stage=%q by=%q", line.Stage, line.By)
	}
}
