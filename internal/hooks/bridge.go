package hooks

import (
	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
)

// PermissionRequestDecision is the resolved hook-stage decision for one
// approval situation — the aggregated result of AggregatePermissionRequest,
// carried across the package boundary into the approval-invariant check.
type PermissionRequestDecision struct {
	Behavior string // "allow" | "deny" | "" (none -> fall through)
	Message  string
}

// ApplyPermissionRequest merges a PermissionRequest hook decision onto a
// policy-stage approval.Result, enforcing agora-spec-approvals.md §4
// invariant 1: "hooks (stage 1) can only be MORE restrictive than policy,
// except an explicit PermissionRequest-hook allow, which is an
// operator-authored bypass and is logged as such."
//
// Returns the final Result and bypassLogged — true exactly when the
// exception fired (a hook allow turned a non-allow base into an allow).
// Callers MUST emit an audit line when bypassLogged is true (this function
// only computes the fact; approval.AuditLine/NewAuditLine — already
// stage-attributed to contracts.StageHook — is how the caller records it,
// same pattern as the rest of the approval package).
//
// PermissionRequest deny ALWAYS applies (tightening, or a no-op over an
// already-deny base) — no logging distinction needed there, same as any
// other more-restrictive hook outcome.
func ApplyPermissionRequest(base approval.Result, decision PermissionRequestDecision) (final approval.Result, bypassLogged bool) {
	switch decision.Behavior {
	case "deny":
		if base.Action == approval.ActionDeny {
			return base, false
		}
		return approval.Result{
			Action:  approval.ActionDeny,
			Kind:    base.Kind,
			Stage:   contracts.StageHook,
			By:      "hook:permission-request",
			Message: decision.Message,
		}, false

	case "allow":
		if base.Action == approval.ActionAllow {
			return base, false // no loosening actually occurred
		}
		// The ONLY loosening path invariant 1 permits — must be logged.
		return approval.Result{
			Action:  approval.ActionAllow,
			Kind:    base.Kind,
			Scope:   contracts.ScopeOnce,
			Stage:   contracts.StageHook,
			By:      "hook:permission-request:bypass",
			Message: decision.Message,
		}, true

	default:
		// No decision -> fall through to normal approval flow (§2.2):
		// base is returned unchanged, unmodified by the hook stage.
		return base, false
	}
}
