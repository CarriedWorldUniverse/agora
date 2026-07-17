package hooks

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
)

// TestApplyPermissionRequest_TightensOnly proves agora-spec-approvals.md §4
// invariant 1's restricting half: a hook deny always applies, whatever the
// policy-stage base was.
func TestApplyPermissionRequest_TightensOnly(t *testing.T) {
	base := approval.Result{Action: approval.ActionAllow, Kind: contracts.KindExec, Stage: contracts.StagePolicy, By: "policy"}
	final, bypassLogged := ApplyPermissionRequest(base, PermissionRequestDecision{Behavior: "deny", Message: "hook says no"})
	if final.Action != approval.ActionDeny {
		t.Fatalf("Action = %v, want deny — a hook deny always tightens", final.Action)
	}
	if bypassLogged {
		t.Error("a deny is a tightening, never a bypass — bypassLogged must be false")
	}
	if final.Stage != contracts.StageHook {
		t.Errorf("Stage = %v, want StageHook", final.Stage)
	}
}

// TestApplyPermissionRequest_AllowIsTheOnlyLoosening_MustBeLogged proves
// invariant 1's sole exception: an explicit PermissionRequest-hook allow
// over a non-allow base is the only loosening path, and it MUST be
// reported as a logged bypass.
func TestApplyPermissionRequest_AllowIsTheOnlyLoosening_MustBeLogged(t *testing.T) {
	base := approval.Result{Action: approval.ActionAsk, Kind: contracts.KindExec, Stage: contracts.StagePolicy, By: "policy"}
	final, bypassLogged := ApplyPermissionRequest(base, PermissionRequestDecision{Behavior: "allow", Message: "operator pre-authorized"})
	if final.Action != approval.ActionAllow {
		t.Fatalf("Action = %v, want allow", final.Action)
	}
	if !bypassLogged {
		t.Fatal("an allow that loosens a non-allow base MUST be reported as a logged bypass")
	}
	if final.By != "hook:permission-request:bypass" {
		t.Errorf("By = %q, want an actor string that clearly marks this as a hook bypass", final.By)
	}
}

// TestApplyPermissionRequest_AllowOverAllowBaseIsNotALoosening: if the
// policy stage already allowed, a hook allow changes nothing and is not a
// bypass (no loosening actually happened).
func TestApplyPermissionRequest_AllowOverAllowBaseIsNotALoosening(t *testing.T) {
	base := approval.Result{Action: approval.ActionAllow, Kind: contracts.KindExec, Stage: contracts.StagePolicy, By: "policy"}
	final, bypassLogged := ApplyPermissionRequest(base, PermissionRequestDecision{Behavior: "allow"})
	if bypassLogged {
		t.Error("no loosening occurred (base was already allow) — must not be logged as a bypass")
	}
	if final.Action != approval.ActionAllow {
		t.Errorf("Action = %v, want allow (unchanged)", final.Action)
	}
}

// TestApplyPermissionRequest_DenyOverDenyBaseIsNoOp: a hook deny over an
// already-denied base changes nothing (still not a bypass — deny never is).
func TestApplyPermissionRequest_DenyOverDenyBaseIsNoOp(t *testing.T) {
	base := approval.Result{Action: approval.ActionDeny, Kind: contracts.KindExec, Stage: contracts.StagePolicy, By: "policy", Message: "orig"}
	final, bypassLogged := ApplyPermissionRequest(base, PermissionRequestDecision{Behavior: "deny", Message: "hook reason"})
	if bypassLogged {
		t.Error("deny is never a bypass")
	}
	if final != base {
		t.Errorf("a deny-over-deny base is a no-op, got %+v want unchanged %+v", final, base)
	}
}

// TestApplyPermissionRequest_NoDecisionFallsThrough: §2.2 "no decision ->
// fall through to normal approval flow" — the base Result passes through
// completely unmodified.
func TestApplyPermissionRequest_NoDecisionFallsThrough(t *testing.T) {
	base := approval.Result{Action: approval.ActionAsk, Kind: contracts.KindEscalation, Stage: contracts.StagePolicy, By: "policy"}
	final, bypassLogged := ApplyPermissionRequest(base, PermissionRequestDecision{})
	if bypassLogged {
		t.Error("no decision at all must never be logged as a bypass")
	}
	if final != base {
		t.Errorf("final = %+v, want the base unchanged", final)
	}
}

// TestApplyPermissionRequest_CannotLoosenAskToAllowWithoutLogging is a
// belt-and-suspenders scan over every non-allow base action, confirming
// the ONLY way to reach ActionAllow is via the logged bypass path.
func TestApplyPermissionRequest_CannotLoosenAskToAllowWithoutLogging(t *testing.T) {
	for _, action := range []approval.Action{approval.ActionDeny, approval.ActionAsk, approval.ActionConvert} {
		base := approval.Result{Action: action, Kind: contracts.KindExec, Stage: contracts.StagePolicy, By: "policy"}
		final, bypassLogged := ApplyPermissionRequest(base, PermissionRequestDecision{Behavior: "allow"})
		if action == approval.ActionDeny {
			// deny is final at policy stage in the approval package's own
			// invariant 1 wording ("deny is final for that call... hooks
			// can only be more restrictive... except an explicit
			// PermissionRequest-hook allow, which IS the logged bypass").
			// This bridge function implements exactly that exception, so a
			// deny base can still be loosened here — but ONLY logged.
		}
		if final.Action == approval.ActionAllow && !bypassLogged {
			t.Errorf("base action %v loosened to allow WITHOUT a logged bypass — invariant 1 violated", action)
		}
	}
}
