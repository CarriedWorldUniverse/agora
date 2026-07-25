package tui

import (
	"errors"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// This file is the modal option→(decision,scope) mapping (agora-spec-tui.md
// §3): given an ApprovalKind, what the list-select modal offers, and how a
// chosen option resolves into the exact contracts.Input triple (Decision,
// Scope, Message/Answer) the io session protocol (U2) and the approvals
// pipeline (U7) expect on the wire.

// ModalOption is one selectable row in the approval modal.
type ModalOption struct {
	Label string
	// Decision/Scope apply to permission-shaped kinds (exec, patch,
	// escalation, mcp_tool, gate).
	Decision contracts.Decision
	Scope    contracts.Scope
	// RequiresMessage: choosing this option routes focus back to the
	// composer so the operator can type feedback before the response is
	// actually sent (§3: "the deny-with-feedback option routes back to the
	// composer with focus so you type guidance").
	RequiresMessage bool
	// Disabled marks an option the operator cannot currently choose (the
	// plan modal's allow option while open_questions remain, §3).
	Disabled    bool
	DisabledWhy string
}

// permissionKinds is the set of approval kinds that render as the
// allow/deny list-select modal (§3): everything except question (a
// structured answer card) and plan (its own modal with the open-questions
// gate).
func isPermissionKind(k contracts.ApprovalKind) bool {
	switch k {
	case contracts.KindExec, contracts.KindPatch, contracts.KindEscalation,
		contracts.KindMCPTool, contracts.KindGate:
		return true
	default:
		return false
	}
}

// ApprovalModalOptions returns the v1 option set (§3) for a permission-shaped
// approval kind: approve once / approve for session / deny-with-feedback.
// Returns nil for kinds that are not permission-shaped (question, plan —
// those have their own option builders below).
func ApprovalModalOptions(kind contracts.ApprovalKind) []ModalOption {
	if !isPermissionKind(kind) {
		return nil
	}
	return []ModalOption{
		{Label: "Approve once", Decision: contracts.DecisionAllow, Scope: contracts.ScopeOnce},
		{Label: "Approve for the rest of this session", Decision: contracts.DecisionAllow, Scope: contracts.ScopeSession},
		{
			Label:           "Deny — tell the agent what to do differently",
			Decision:        contracts.DecisionDeny,
			Scope:           contracts.ScopeOnce,
			RequiresMessage: true,
		},
	}
}

// EscDecision is the option Esc resolves to on a permission-shaped modal:
// every exit is an explicit decision (§3 — "Esc = explicit deny/cancel,
// never silent"), never a no-op that leaves the request unresolved.
func EscDecision() ModalOption {
	return ModalOption{
		Label:    "Esc (deny)",
		Decision: contracts.DecisionDeny,
		Scope:    contracts.ScopeOnce,
	}
}

// ErrMessageRequired is returned by ResolveApproval when option.RequiresMessage
// is set and no message was supplied — the caller should route focus back to
// the composer instead of sending, per §3.
var ErrMessageRequired = errors.New("tui: this option requires typed feedback before it can be sent")

// ResolveApproval builds the exact contracts.Input a permission-shaped
// approval modal selection sends over the wire (InApprovalResponse, id +
// decision + scope + optional message).
func ResolveApproval(requestID string, option ModalOption, message string) (contracts.Input, error) {
	if option.RequiresMessage && message == "" {
		return contracts.Input{}, ErrMessageRequired
	}
	return contracts.Input{
		Type:     contracts.InApprovalResponse,
		ID:       requestID,
		Decision: option.Decision,
		Scope:    option.Scope,
		Message:  message,
	}, nil
}

// QuestionCardChoice is what the operator selected/typed on a question card
// before BuildQuestionAnswer turns it into the wire Input.
type QuestionCardChoice struct {
	Selected []string // option labels chosen
	FreeText string
}

// ErrNoAnswerGiven is returned when a question card submit carries neither a
// selection nor free text.
var ErrNoAnswerGiven = errors.New("tui: question card has no selection or free text")

// ErrTooManySelections is returned when more than one option is chosen on a
// single-select question card.
var ErrTooManySelections = errors.New("tui: question card is single-select but more than one option was chosen")

// BuildQuestionAnswer validates a question card choice against the args the
// question was asked with (agora-spec-planning-questions §4: Choice holds
// ≥1 option labels when options were offered, >1 only when MultiSelect) and
// returns the InQuestionResponse Input.
func BuildQuestionAnswer(requestID string, args contracts.QuestionArgs, choice QuestionCardChoice) (contracts.Input, error) {
	if len(choice.Selected) == 0 && choice.FreeText == "" {
		return contracts.Input{}, ErrNoAnswerGiven
	}
	if len(choice.Selected) > 1 && !args.MultiSelect {
		return contracts.Input{}, ErrTooManySelections
	}
	return contracts.Input{
		Type: contracts.InQuestionResponse,
		ID:   requestID,
		Answer: &contracts.AnswerInput{
			Choice: choice.Selected,
			Text:   choice.FreeText,
		},
	}, nil
}

// declinedAnswerText is what a question card Esc sends: an explicit,
// legible decline rather than a silent no-op or an unresolved request left
// hanging (§3's "every exit is an explicit decision" applied to question
// cards — spec ambiguity: agora-spec-approvals.md's package doc says "a
// deny on a question means declined to answer, with Message as the
// reason", but contracts.Input's question_response shape carries no
// Decision/Message field, only Answer — there is no separate "decline"
// wire state. Resolved here by encoding the decline as answer TEXT, which
// is legitimate content for an Answer (an actor really did answer "I
// decline"), not a bypass of never-fabricate).
const declinedAnswerText = "(declined to answer)"

// EscQuestionAnswer is what Esc on a question card sends.
func EscQuestionAnswer(requestID string) contracts.Input {
	return contracts.Input{
		Type:   contracts.InQuestionResponse,
		ID:     requestID,
		Answer: &contracts.AnswerInput{Text: declinedAnswerText},
	}
}

// PlanModalOption mirrors ModalOption for the plan gate (§3): the allow
// option is disabled while any open_questions remain unresolved,
// regardless of operator intent (planning-questions §3/§6 invariant 6).
type PlanModalOption = ModalOption

// PlanModalOptions returns the plan gate's options given the current set of
// still-open question IDs (unresolvedQuestionIDs — the plan artifact's
// OpenQuestions minus whatever question.answered events have already
// resolved).
func PlanModalOptions(unresolvedQuestionIDs []string) []PlanModalOption {
	allow := PlanModalOption{Label: "Approve plan", Decision: contracts.DecisionAllow, Scope: contracts.ScopeOnce}
	if len(unresolvedQuestionIDs) > 0 {
		allow.Disabled = true
		allow.DisabledWhy = fmt.Sprintf("%d open question(s) must be answered first", len(unresolvedQuestionIDs))
	}
	deny := PlanModalOption{
		Label:           "Deny — tell the agent what to do differently",
		Decision:        contracts.DecisionDeny,
		Scope:           contracts.ScopeOnce,
		RequiresMessage: true,
	}
	return []PlanModalOption{allow, deny}
}

// ResolvePlan builds the InApprovalResponse Input for a plan-gate decision.
// Returns an error (never sends) if the option is Disabled or requires a
// message that wasn't supplied.
func ResolvePlan(requestID string, option PlanModalOption, message string) (contracts.Input, error) {
	if option.Disabled {
		return contracts.Input{}, fmt.Errorf("tui: plan option %q is disabled: %s", option.Label, option.DisabledWhy)
	}
	return ResolveApproval(requestID, option, message)
}
