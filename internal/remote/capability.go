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
