// Capability enforcement: the auth backstop internal/io's capability gate
// presumes (io/session.go: "this is NOT the U16 auth-deferral question of
// who may DECLARE a capability in the first place"). This package answers
// that question — a session's Capabilities always come from the
// AUTHENTICATED device's registry grant, never from client-declared input.
// Spec: agora-spec-remote.md §4.
package remote

import (
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// AttachInfo builds the io.AttachInfo for an authenticated device — the
// capabilities set here are the device's REGISTRY grant, ignoring anything
// a client might otherwise try to declare on the wire. This is what closes
// io/session.go's DEFERRED note: "nothing stops one client from claiming
// another's client_id ... Verifying/assigning identity is U16's job."
// clientID is the device's fingerprint (Device.ID) — the one identity a
// client cannot forge once it has passed the handshake.
func AttachInfo(device Device, kind string) agoraio.AttachInfo {
	return agoraio.AttachInfo{
		ClientID:     device.ID,
		Kind:         kind,
		Capabilities: append([]contracts.Capability(nil), device.Capabilities...),
	}
}

// CheckInput reports whether device may send an input of type t, per
// contracts.RequiredForInput/Holds (spec §4). This mirrors the check
// io.Session.handleInput performs internally on the AttachInfo it was
// handed — calling it here BEFORE constructing that AttachInfo lets a
// caller refuse (and never even attach) a device whose grant plainly
// cannot exercise the input it is asking to attach for, with a
// remote-specific error (ErrCapabilityDenied) instead of io's.
func CheckInput(device Device, t contracts.InputType) error {
	if !contracts.Holds(device.Capabilities, contracts.RequiredForInput(t)) {
		return fmt.Errorf("%w: input %q needs %q", ErrCapabilityDenied, t, contracts.RequiredForInput(t))
	}
	return nil
}

// CheckApproval reports whether device may resolve an approval of kind k:
// it must hold the capability contracts.RequiredForApproval(k) requires,
// AND — if the device's enrollment narrows AllowedApprovalKinds — k must
// be in that explicit allow-list (spec §4: "approval kinds (exec yes,
// patch no)"). A device with CapApprover but an AllowedApprovalKinds
// constraint that omits k is refused even though its capability TIER would
// otherwise permit it — constraints only ever narrow a grant, never widen
// it.
func CheckApproval(device Device, k contracts.ApprovalKind) error {
	need := contracts.RequiredForApproval(k)
	if !contracts.Holds(device.Capabilities, need) {
		return fmt.Errorf("%w: approval kind %q needs %q", ErrCapabilityDenied, k, need)
	}
	if len(device.Constraints.AllowedApprovalKinds) > 0 && !kindAllowed(device.Constraints.AllowedApprovalKinds, k) {
		return fmt.Errorf("%w: approval kind %q not in device's allowed-kinds constraint", ErrCapabilityDenied, k)
	}
	return nil
}

func kindAllowed(allowed []contracts.ApprovalKind, k contracts.ApprovalKind) bool {
	for _, a := range allowed {
		if a == k {
			return true
		}
	}
	return false
}

// CheckProfile reports whether device may switch to/use profile, per its
// registry-granted AllowedProfiles constraint (spec §4: "vessel bound to
// the chat profile only"). Mirrors CheckApproval's narrow-only semantics:
// an empty (nil or zero-length) AllowedProfiles means "no narrowing on this
// axis" — allow. A non-empty constraint is an exact allow-list; profile
// must be a member or this fails closed with ErrProfileNotAllowed.
//
// HANDOFF NOTE (U16 -> U18): this package has no in-unit call site that
// performs a profile switch — that decision lives in the daemon wiring
// (U18). U18's profile-switch path MUST call CheckProfile before honoring
// a switch, or this constraint has zero effect despite being stored and
// tested here.
func CheckProfile(device Device, profile string) error {
	if len(device.Constraints.AllowedProfiles) > 0 && !stringAllowed(device.Constraints.AllowedProfiles, profile) {
		return fmt.Errorf("%w: profile %q not in device's allowed-profiles constraint", ErrProfileNotAllowed, profile)
	}
	return nil
}

// CheckThread reports whether device may attach to threadID, per its
// registry-granted AllowedThreads constraint (spec §4). Mirrors
// CheckProfile/CheckApproval's narrow-only semantics: an empty
// AllowedThreads means unconstrained; a non-empty one is an exact
// allow-list, else ErrThreadNotAllowed (fail closed).
//
// HANDOFF NOTE (U16 -> U18): same as CheckProfile — the thread-attach
// decision is made in U18's daemon wiring, not in this package. U18's
// attach path MUST call CheckThread before honoring an attach, or this
// constraint has zero effect despite being stored and tested here.
func CheckThread(device Device, threadID string) error {
	if len(device.Constraints.AllowedThreads) > 0 && !stringAllowed(device.Constraints.AllowedThreads, threadID) {
		return fmt.Errorf("%w: thread %q not in device's allowed-threads constraint", ErrThreadNotAllowed, threadID)
	}
	return nil
}

func stringAllowed(allowed []string, v string) bool {
	for _, a := range allowed {
		if a == v {
			return true
		}
	}
	return false
}
