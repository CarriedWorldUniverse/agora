package turnengine

import (
	"encoding/json"
	"testing"
	"time"

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

// TestManager_AbandonedApprovalIsAudited covers agora#150: a gated call that
// did NOT execute must leave a record saying so.
//
// An ActionAsk was recorded only once the operator ANSWERED it, so a pending
// ask abandoned by an interrupt wrote nothing at all. The durable trail could
// therefore only ever contain allows — a live thread's audit read 323
// decisions, every one an allow, in a session where a call had in fact been
// refused and did not run. An audit that structurally cannot record a refusal
// cannot answer "what has this thing been stopped from doing?", nor tell
// "never attempted" apart from "attempted, gated, and killed".
//
// TestManager_Approval_InterruptDuringPendingApproval already drove exactly
// this sequence but configured no store and asserted nothing about the audit,
// which is why the hole was invisible.
func TestManager_AbandonedApprovalIsAudited(t *testing.T) {
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_abandon"}); err != nil {
		t.Fatal(err)
	}
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "never written")}},
		fake.Step{Text: "unreachable"},
	)
	_, in, out, runErr := newTestManagerWithStore(t, "th_abandon", store, provider,
		WithRoots(managerTestRoots(t)), WithPolicy(promptAllPolicy()),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}
	recvApprovalRequested(t, out, testTimeout)

	// Interrupt INSTEAD of answering — the operator walking away from the
	// prompt, which is the common shape of this.
	in <- contracts.Input{Type: contracts.InInterrupt}

	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				break loop
			}
			if ev.Type == contracts.EvTurnFailed {
				break loop
			}
		case <-deadline:
			t.Fatal("turn never failed after the interrupt")
		}
	}
	endAndClose(t, in, out, runErr)

	it, err := store.Resume("th_abandon")
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
	if len(audits) == 0 {
		t.Fatal("no approval_decision item after an abandoned ask — the call was gated and never ran, and the trail says nothing (agora#150)")
	}

	var line struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
		By     string `json:"by"`
	}
	b, err := json.Marshal(audits[len(audits)-1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &line); err != nil {
		t.Fatalf("decode audit line: %v (%s)", err, b)
	}
	if line.Action != "deny" {
		t.Errorf("action = %q; want \"deny\" — the call did not execute", line.Action)
	}
	// The actor must distinguish an abandonment from an operator's explicit
	// "no", or the two are conflated by anyone reading the trail.
	if line.By != abortInterrupted {
		t.Errorf("by = %q; want %q so an abandonment is never read as a deliberate refusal", line.By, abortInterrupted)
	}
}
