package contracts

// Capability is a controller's authority tier — assigned at device enrollment,
// inherited by the session, checked on every inbound message.
// Spec: agora-spec-remote.md §4.
type Capability string

const (
	// CapObserver receives events only.
	CapObserver Capability = "observer"
	// CapInteractive sends user_message/steer/interrupt AND answers
	// question-kind cards (structured answers).
	CapInteractive Capability = "interactive"
	// CapApprover answers permission/plan/gate approvals (grantable without
	// interactive: a phone that approves but cannot steer).
	CapApprover Capability = "approver"
	// CapAdmin: device management, profile switching, daemon config, provision.
	CapAdmin Capability = "admin"
)

// RequiredForInput maps each inbound message type to the capability a client
// must hold to send it. Compiling the capability model (rather than leaving it
// in prose) is what stops U2/U16 from wiring the wrong gate silently.
// Spec: agora-spec-remote.md §4, agora-spec-io.md §0a/§1, agora-spec-approvals.md §4.
func RequiredForInput(t InputType) Capability {
	switch t {
	case InUserMessage, InSteer, InInterrupt:
		return CapInteractive
	case InQuestionResponse:
		// A question answer is interactive, not approver (approvals §3).
		return CapInteractive
	case InApprovalResponse:
		// The DECISION kind decides: plan/gate/permission need approver;
		// this is the floor for a generic approval_response. The resolver
		// refines by the referenced request's kind (RequiredForApproval).
		return CapApprover
	case InConfig, InProvision:
		return CapAdmin
	case InEnd:
		return CapInteractive
	default:
		// Unknown input types fail closed at the highest bar.
		return CapAdmin
	}
}

// RequiredForApproval maps an approval kind to who may answer it: questions are
// interactive, everything gate-shaped is approver.
// Spec: agora-spec-approvals.md §4 invariant 5, TUI approval modal.
func RequiredForApproval(k ApprovalKind) Capability {
	if k == KindQuestion {
		return CapInteractive
	}
	return CapApprover
}

// Holds reports whether a set of granted capabilities satisfies need. Tiers do
// NOT nest implicitly (approver without interactive is a valid, deliberate
// grant — remote §4), so membership is exact, not ordinal.
func Holds(granted []Capability, need Capability) bool {
	for _, g := range granted {
		if g == need {
			return true
		}
	}
	return false
}
