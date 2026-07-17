package planning

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// PlanLog is the plan artifact's append-only revision history: every
// `plan` tool update is a NEW contracts.TIPlanRevision thread item, never a
// rewrite. Current replays the thread to find the latest revision — the
// thread's own persisted log (any contracts.ThreadStore) IS the durable
// state; there is no separate index to lose.
// Spec: agora-spec-planning-questions.md §1.
type PlanLog struct {
	store contracts.ThreadStore
}

// NewPlanLog builds a PlanLog over store (persistence.LocalStore for real
// durability, persistence.MemStore for tests/ephemeral pods).
func NewPlanLog(store contracts.ThreadStore) *PlanLog {
	return &PlanLog{store: store}
}

// Update appends a new plan revision — the `plan` tool call's effect.
// Streamed to clients as an `item.*` event of ItemType plan by the io
// layer (a later/other unit); this call is the durable side of that.
func (l *PlanLog) Update(threadID string, p contracts.PlanArtifact, ts time.Time, identity string) error {
	if err := l.store.Append(threadID, []contracts.ThreadItem{{
		TS: ts, Type: contracts.TIPlanRevision, Identity: identity, Payload: p,
	}}); err != nil {
		return fmt.Errorf("planning: append plan revision: %w", err)
	}
	return nil
}

// Current replays threadID's log and returns the latest plan revision, and
// whether one exists at all (found=false for a thread that never called
// the plan tool).
func (l *PlanLog) Current(threadID string) (p contracts.PlanArtifact, found bool, err error) {
	it, err := l.store.Resume(threadID)
	if err != nil {
		return contracts.PlanArtifact{}, false, fmt.Errorf("planning: resume for plan log: %w", err)
	}
	defer it.Close()

	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type != contracts.TIPlanRevision {
			continue
		}
		rev, decErr := decodePlanArtifact(item.Payload)
		if decErr != nil {
			return contracts.PlanArtifact{}, false, fmt.Errorf("planning: decode plan revision seq %d: %w", item.Seq, decErr)
		}
		p, found = rev, true
	}
	if err := it.Err(); err != nil {
		return contracts.PlanArtifact{}, false, fmt.Errorf("planning: replay plan log: %w", err)
	}
	return p, found, nil
}

func decodePlanArtifact(payload any) (contracts.PlanArtifact, error) {
	var p contracts.PlanArtifact
	b, err := json.Marshal(payload)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	return p, nil
}

// GateRequest is the plan gate's input: the submitted plan artifact
// (Submit must be true — that is what raises the KindPlan approval in the
// first place, §3) plus the operator's decision on the gate card. Exit
// authority is the operator's: By/Message record the recorded decision
// (approving the gate card, an explicit mode change, or a conversational
// go-signal — all become an allow decision with the message as evidence).
type GateRequest struct {
	Plan     contracts.PlanArtifact
	Decision contracts.Decision
	// Exit is the requested exit; only consulted when Decision == DecisionAllow.
	Exit contracts.PlanExit
	// By is the deciding actor (device/identity fingerprint). Plan is always
	// approver-gated (contracts §1 invariant 5) — there is no hook/preset
	// "by" here the way a permission kind's policy-auto allow has one.
	By string
	// Message: on deny, feedback for the revision loop; on allow, the
	// evidence recorded for the exit decision (e.g. a conversational
	// go-signal transcript, or an explicit delegation answer to an open
	// question — §3).
	Message string
}

// GateOutcome is the resolved plan-gate decision.
type GateOutcome struct {
	Resolution contracts.ApprovalResolution
	// Exit is set only when the gate actually passed
	// (Resolution.Decision == DecisionAllow).
	Exit contracts.PlanExit
	// Revise is true when the posture stays and the model must revise and
	// re-raise: a plain operator deny, OR an attempted allow refused by the
	// open-questions invariant — the operator's allow does not get to
	// bypass §3/§6 invariant 6.
	Revise bool
}

// Gate resolves one plan-gate approval. It never fabricates an exit: an
// allow with open_questions non-empty is refused — turned into a deny +
// revision — regardless of operator intent (invariant 6). Resolving the
// questions (including an explicit "your call" delegation, itself a
// recorded Answer, planning-questions §3) must precede any exit; Gate does
// not itself resolve questions, it only enforces that none remain open.
// Spec: agora-spec-planning-questions.md §3, §6 invariant 6.
func Gate(req GateRequest) (GateOutcome, error) {
	if !req.Plan.Submit {
		return GateOutcome{}, ErrPlanNotSubmitted
	}

	if req.Decision == contracts.DecisionAllow && len(req.Plan.OpenQuestions) > 0 {
		return GateOutcome{
			Resolution: contracts.ApprovalResolution{
				Decision: contracts.DecisionDeny,
				Message:  ErrOpenQuestions.Error(),
				By:       req.By,
				Stage:    contracts.StageApprover,
			},
			Revise: true,
		}, ErrOpenQuestions
	}

	switch req.Decision {
	case contracts.DecisionAllow:
		if req.Exit != contracts.ExitInline && req.Exit != contracts.ExitDelegate {
			return GateOutcome{}, fmt.Errorf("%w: %q", ErrUnknownExit, req.Exit)
		}
		return GateOutcome{
			Resolution: contracts.ApprovalResolution{
				Decision: contracts.DecisionAllow,
				Message:  req.Message,
				By:       req.By,
				Stage:    contracts.StageApprover,
			},
			Exit: req.Exit,
		}, nil

	case contracts.DecisionDeny:
		return GateOutcome{
			Resolution: contracts.ApprovalResolution{
				Decision: contracts.DecisionDeny,
				Message:  req.Message,
				By:       req.By,
				Stage:    contracts.StageApprover,
			},
			Revise: true,
		}, nil

	default:
		return GateOutcome{}, fmt.Errorf("%w: %q", ErrUnknownDecision, req.Decision)
	}
}
