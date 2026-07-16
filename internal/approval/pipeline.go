package approval

import "github.com/CarriedWorldUniverse/agora/contracts"

// Action is the pipeline's outcome for a request — a superset of
// contracts.Decision (allow/deny) that also covers the two non-final
// outcomes: ask (escalate to a human approver, stage 3+) and convert (the
// question-only exception, §2 footnote †).
type Action string

const (
	// ActionAllow: the request may proceed.
	ActionAllow Action = "allow"
	// ActionDeny: the request is refused. Final for that call (invariant 1).
	ActionDeny Action = "deny"
	// ActionAsk: policy could not resolve the call alone; it escalates to an
	// approver (stage 3, out of this package's scope).
	ActionAsk Action = "ask"
	// ActionConvert: KindQuestion under a convert policy — parks (threads)
	// or terminates blocked:needs-input (one-shot pods), per the escalation
	// ladder. Never auto-answered, never silently denied.
	ActionConvert Action = "convert"
)

// Request is a single approval situation to resolve: kind + the scope
// context needed to check for a prior scoped allow. Request carries no
// payload (exec command, diff footprint, etc.) — that lives in the
// contracts.ApprovalRequest this package's caller builds around the
// Result; Decide only needs enough to resolve policy + scope.
type Request struct {
	// ID correlates this decision to its audit line / the wire
	// ApprovalRequest.ID. Not used for matching.
	ID string
	// Kind is what's being asked (contracts.ApprovalKind).
	Kind contracts.ApprovalKind
	// SessionID is the thread/session identity — the exact key a
	// contracts.ScopeSession allow matches against.
	SessionID string
	// ScopeKey is the exact key a contracts.ScopePrefix or contracts.ScopeHost
	// allow matches against (e.g. a caller-derived command-prefix token or
	// host pattern). Deriving this key from raw request content (tokenizing
	// a command line, normalizing a host) is the CALLER's job — see
	// scope.go's doc comment for why this package only ever does exact-key
	// matching.
	ScopeKey string
}

// Result is the resolved outcome of Decide, carrying everything the audit
// line needs (invariant 3: every decision records stage + actor).
type Result struct {
	Action Action
	Kind   contracts.ApprovalKind
	// Scope is set when Action == ActionAllow: contracts.ScopeOnce for a
	// fresh policy-auto allow, or the scope of the grant that matched for a
	// short-circuited allow.
	Scope contracts.Scope
	// Stage is which pipeline stage produced this Result: StagePolicy for a
	// policy-only resolution, StageApprover when a prior scoped allow
	// (originally granted by an approver) short-circuited it.
	Stage contracts.Stage
	// By is the deciding actor: "policy" for a plain policy resolution, or
	// the approver identity carried on the matched scope grant.
	By string
	// Message is deny feedback to the model, mirroring
	// contracts.ApprovalResolution.Message.
	Message string
}

// Resolution converts a final Result (allow/deny) into a
// contracts.ApprovalResolution for the given request id. ok is false for
// ask/convert, which contracts.Decision cannot represent — those outcomes
// are not yet a final permission decision.
func (r Result) Resolution(id string) (contracts.ApprovalResolution, bool) {
	var dec contracts.Decision
	switch r.Action {
	case ActionAllow:
		dec = contracts.DecisionAllow
	case ActionDeny:
		dec = contracts.DecisionDeny
	default:
		return contracts.ApprovalResolution{}, false
	}
	return contracts.ApprovalResolution{
		ID:       id,
		Decision: dec,
		Scope:    r.Scope,
		Message:  r.Message,
		By:       r.By,
		Stage:    r.Stage,
	}, true
}

// knownKinds is the compile-visible set of recognized ApprovalKind values
// (contracts §1). A kind outside this set is treated as the most dangerous
// possible request — fail closed, never allow (ground rule 5).
var knownKinds = map[contracts.ApprovalKind]bool{
	contracts.KindExec:       true,
	contracts.KindPatch:      true,
	contracts.KindEscalation: true,
	contracts.KindMCPTool:    true,
	contracts.KindQuestion:   true,
	contracts.KindPlan:       true,
	contracts.KindGate:       true,
}

// Decide resolves req against policy (a preset or custom contracts.PolicySet)
// and store (nil is fine — it just skips the scope short-circuit,
// equivalent to an always-empty store). It never errors: every input,
// however malformed, resolves to a Result, and the SAFE side is always
// chosen when resolution is ambiguous (ground rule 5).
//
// Precedence (spec §2/§4):
//  1. Unknown kind → deny (fail closed on the worst case).
//  2. contracts.KindGate is deliberately preset-ungoverned (§2 note: no gate
//     column — gate always surfaces to a human) → ask.
//  3. Missing/malformed policy value for a known kind → fail closed: ask
//     (never allow; never deny a question — see 6).
//  4. PolicyDeny → deny. Final for that call (invariant 1); a scope store is
//     not even consulted, so a stray/stale scope grant can never override a
//     hard deny.
//  5. PolicyAuto → allow, scope once.
//  6. PolicyConvert, only valid on KindQuestion → convert. On any other kind
//     it is a misconfiguration and fails closed to ask (never allow, never
//     convert something that isn't a question).
//  7. PolicyPerServer, only valid on KindMCPTool → this unit does not
//     resolve per-server MCP approval modes (that's the MCP unit's job), so
//     it fails closed to ask, deferring the actual decision. On any other
//     kind: misconfiguration, fails closed to ask.
//  8. PolicyPrompt → check the scope store for a prior matching allow
//     (session, then prefix/host); if found, short-circuit to allow at
//     StageApprover attributed to the original grant. Otherwise ask.
//  9. KindQuestion is NEVER resolved to ActionDeny by this function,
//     regardless of what a (misconfigured) policy set says for it —
//     invariant 2's "never deny-fabricates" is enforced here as a property
//     of Decide, not left to preset authors to get right.
func Decide(policy contracts.PolicySet, req Request, store ScopeStore) Result {
	if !knownKinds[req.Kind] {
		return Result{
			Action:  ActionDeny,
			Kind:    req.Kind,
			Stage:   contracts.StagePolicy,
			By:      "policy:unknown-kind",
			Message: "approval: unrecognized approval kind, fail-closed deny",
		}
	}

	if req.Kind == contracts.KindGate {
		return Result{
			Action: ActionAsk,
			Kind:   req.Kind,
			Stage:  contracts.StagePolicy,
			By:     "policy:gate-always-asks",
		}
	}

	v, ok := policy[req.Kind]
	if !ok {
		return failClosedAsk(req.Kind, "policy:missing-fail-closed", "approval: policy set has no entry for this kind, fail-closed ask")
	}

	switch v {
	case contracts.PolicyDeny:
		if req.Kind == contracts.KindQuestion {
			// Invariant 2: a question is never denied. Treat a misconfigured
			// deny-on-question the same as a malformed value: fail closed to
			// ask, never fabricate an answer, never silently refuse.
			return failClosedAsk(req.Kind, "policy:question-never-denied", "approval: question policy cannot be deny, fail-closed ask")
		}
		return Result{
			Action:  ActionDeny,
			Kind:    req.Kind,
			Stage:   contracts.StagePolicy,
			By:      "policy",
			Message: "denied by policy",
		}

	case contracts.PolicyAuto:
		return Result{
			Action: ActionAllow,
			Kind:   req.Kind,
			Scope:  contracts.ScopeOnce,
			Stage:  contracts.StagePolicy,
			By:     "policy",
		}

	case contracts.PolicyConvert:
		if req.Kind != contracts.KindQuestion {
			return failClosedAsk(req.Kind, "policy:convert-misapplied", "approval: convert is only valid for question, fail-closed ask")
		}
		return Result{
			Action: ActionConvert,
			Kind:   req.Kind,
			Stage:  contracts.StagePolicy,
			By:     "policy",
		}

	case contracts.PolicyPerServer:
		if req.Kind != contracts.KindMCPTool {
			return failClosedAsk(req.Kind, "policy:per-server-misapplied", "approval: per-server is only valid for mcp_tool, fail-closed ask")
		}
		// The actual per-server approval_mode resolution is the MCP unit's
		// job (agora-spec-mcp.md §1); this package defers rather than
		// guessing, and deferring safely means asking, never allowing.
		return Result{
			Action: ActionAsk,
			Kind:   req.Kind,
			Stage:  contracts.StagePolicy,
			By:     "policy:per-server-deferred",
		}

	case contracts.PolicyPrompt:
		if store != nil {
			if allow, matched := store.Match(req.Kind, req.SessionID, req.ScopeKey); matched {
				return Result{
					Action: ActionAllow,
					Kind:   req.Kind,
					Scope:  allow.Scope,
					Stage:  contracts.StageApprover,
					By:     allow.By,
				}
			}
		}
		return Result{
			Action: ActionAsk,
			Kind:   req.Kind,
			Stage:  contracts.StagePolicy,
			By:     "policy",
		}

	default:
		// Garbage/unrecognized PolicyValue string: fail closed.
		return failClosedAsk(req.Kind, "policy:malformed-value", "approval: unrecognized policy value, fail-closed ask")
	}
}

func failClosedAsk(kind contracts.ApprovalKind, by, message string) Result {
	return Result{
		Action:  ActionAsk,
		Kind:    kind,
		Stage:   contracts.StagePolicy,
		By:      by,
		Message: message,
	}
}
