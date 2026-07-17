package remote

import (
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func fakeClock(start time.Time) (Clock, func(time.Duration)) {
	now := start
	return func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }
}

// TestQueueTimeoutDeniesPermissionKinds: the stage-3+ approver queue's
// timeout fallback is deny (spec §8: "default deny after 15 min").
func TestQueueTimeoutDeniesPermissionKinds(t *testing.T) {
	start := time.Unix(0, 0)
	clock, advance := fakeClock(start)
	q := NewQueue(clock, 15*time.Minute)

	for _, k := range []contracts.ApprovalKind{
		contracts.KindExec, contracts.KindPatch, contracts.KindEscalation,
		contracts.KindMCPTool, contracts.KindPlan, contracts.KindGate,
	} {
		if err := q.Enqueue(string(k), k); err != nil {
			t.Fatalf("Enqueue %q: %v", k, err)
		}
	}

	// Before timeout: nothing resolves.
	if got := q.Sweep(start.Add(14 * time.Minute)); len(got) != 0 {
		t.Fatalf("Sweep before timeout: got %d resolutions, want 0", len(got))
	}

	advance(15 * time.Minute)
	got := q.Sweep(clock())
	if len(got) != 6 {
		t.Fatalf("Sweep at timeout: got %d resolutions, want 6", len(got))
	}
	for _, r := range got {
		if r.Decision != contracts.DecisionDeny {
			t.Errorf("id %q: decision %q, want deny", r.ID, r.Decision)
		}
		if r.Stage != contracts.StageTimeout {
			t.Errorf("id %q: stage %q, want %q", r.ID, r.Stage, contracts.StageTimeout)
		}
		if r.By != "timeout" {
			t.Errorf("id %q: by %q, want %q", r.ID, r.By, "timeout")
		}
	}

	// A second sweep resolves nothing more (already resolved, not
	// re-emitted).
	if got := q.Sweep(clock()); len(got) != 0 {
		t.Fatalf("second Sweep: got %d resolutions, want 0 (already resolved)", len(got))
	}
}

// TestQueueQuestionNeverTimeoutDenies: kind question PARKS instead of
// timeout-denying — invariant 2 / spec §8's explicit exception.
func TestQueueQuestionNeverTimeoutDenies(t *testing.T) {
	start := time.Unix(0, 0)
	clock, advance := fakeClock(start)
	q := NewQueue(clock, 15*time.Minute)

	if err := q.Enqueue("q1", contracts.KindQuestion); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	advance(15 * time.Minute)
	got := q.Sweep(clock())
	if len(got) != 0 {
		t.Fatalf("Sweep on a question at timeout: got %d resolutions, want 0 (never deny-fabricate)", len(got))
	}
	if !q.Parked("q1") {
		t.Fatalf("question should be parked after timeout")
	}
	if q.Resolved("q1") {
		t.Fatalf("a parked question is not resolved — it stays queued for an eventual answer")
	}

	// Sweeping repeatedly (simulating many timer ticks) never flips it to
	// resolved/denied.
	advance(24 * time.Hour)
	if got := q.Sweep(clock()); len(got) != 0 {
		t.Fatalf("Sweep long after timeout on a parked question: got %d resolutions, want 0", len(got))
	}

	// An answer can still arrive after parking (§8 reattach-replay path:
	// the device sees the pending, still-queued card and answers).
	dev := Device{ID: "dev1", Capabilities: []contracts.Capability{contracts.CapInteractive}}
	ans, err := q.AnswerQuestion("q1", dev, contracts.AnswerInput{Text: "yes"})
	if err != nil {
		t.Fatalf("AnswerQuestion after park: %v", err)
	}
	if ans.By != "dev1" {
		t.Errorf("Answer.By: got %q want dev1", ans.By)
	}
	if !q.Resolved("q1") {
		t.Fatalf("question should be resolved after being answered")
	}
}

// TestQueueResolveRefusesQuestionKind: permission-shaped Resolve must not
// be usable to answer a question (a Decision cannot represent an Answer).
func TestQueueResolveRefusesQuestionKind(t *testing.T) {
	q := NewQueue(nil, time.Minute)
	if err := q.Enqueue("q1", contracts.KindQuestion); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	dev := Device{ID: "dev1", Capabilities: []contracts.Capability{contracts.CapInteractive}}
	if _, err := q.Resolve("q1", dev, contracts.DecisionAllow, ""); !errors.Is(err, ErrQuestionNeedsAnswer) {
		t.Fatalf("Resolve on a question: got %v want ErrQuestionNeedsAnswer", err)
	}
}

// TestQueueResolveGatedByCapability: over-reach on the approval queue is
// refused, mirroring the same capability matrix Resolve enforces.
func TestQueueResolveGatedByCapability(t *testing.T) {
	q := NewQueue(nil, time.Minute)
	if err := q.Enqueue("e1", contracts.KindExec); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	weak := Device{ID: "dev1", Capabilities: []contracts.Capability{contracts.CapInteractive}}
	if _, err := q.Resolve("e1", weak, contracts.DecisionAllow, ""); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("Resolve without approver: got %v want ErrCapabilityDenied", err)
	}
	strong := Device{ID: "dev2", Capabilities: []contracts.Capability{contracts.CapApprover}}
	res, err := q.Resolve("e1", strong, contracts.DecisionAllow, "")
	if err != nil {
		t.Fatalf("Resolve with approver: %v", err)
	}
	if res.By != "dev2" || res.Stage != contracts.StageApprover {
		t.Errorf("resolution attribution: got By=%q Stage=%q", res.By, res.Stage)
	}
}

// TestQueueFirstAnswerWins: a second Resolve on an already-resolved id is
// refused (first-answer-wins, spec §0a/§5).
func TestQueueFirstAnswerWins(t *testing.T) {
	q := NewQueue(nil, time.Minute)
	if err := q.Enqueue("e1", contracts.KindExec); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	dev := Device{ID: "dev1", Capabilities: []contracts.Capability{contracts.CapApprover}}
	if _, err := q.Resolve("e1", dev, contracts.DecisionAllow, ""); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if _, err := q.Resolve("e1", dev, contracts.DecisionDeny, ""); !errors.Is(err, ErrApprovalAlreadyResolved) {
		t.Fatalf("second Resolve: got %v want ErrApprovalAlreadyResolved", err)
	}
}

// TestQueueEnqueueRefusesDuplicateID.
func TestQueueEnqueueRefusesDuplicateID(t *testing.T) {
	q := NewQueue(nil, time.Minute)
	if err := q.Enqueue("id1", contracts.KindExec); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if err := q.Enqueue("id1", contracts.KindExec); !errors.Is(err, ErrApprovalAlreadyQueued) {
		t.Fatalf("dup Enqueue: got %v want ErrApprovalAlreadyQueued", err)
	}
}
