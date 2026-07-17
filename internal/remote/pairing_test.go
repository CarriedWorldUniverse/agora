package remote

import (
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestPairingHappyPath: issue -> claim -> approve -> the device is
// enrolled with the default capability set (spec §3).
func TestPairingHappyPath(t *testing.T) {
	clock, _ := fakeClock(time.Unix(0, 0))
	reg := NewRegistry(clock)
	pm := NewPairingManager(clock, reg)

	code, err := pm.Issue(5 * time.Minute)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if pm.Status(code.Code) != StatusPending {
		t.Fatalf("Status after Issue: got %q want pending", pm.Status(code.Code))
	}

	deviceKey := mustKey(t)
	meta := DeviceMetadata{DisplayName: "jacinta's phone", DeviceType: "mobile"}
	if err := pm.Claim(code.Code, deviceKey.PublicBytes(), meta); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if pm.Status(code.Code) != StatusClaimed {
		t.Fatalf("Status after Claim: got %q want claimed", pm.Status(code.Code))
	}

	d, err := pm.Approve(code.Code, nil)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if d.ID != Fingerprint(deviceKey.PublicBytes()) {
		t.Errorf("enrolled id: got %q", d.ID)
	}
	wantCaps := DefaultCapabilities()
	if len(d.Capabilities) != len(wantCaps) {
		t.Errorf("default capabilities: got %v want %v", d.Capabilities, wantCaps)
	}
	if pm.Status(code.Code) != StatusApproved {
		t.Fatalf("Status after Approve: got %q want approved", pm.Status(code.Code))
	}

	got, ok := reg.Get(d.ID)
	if !ok || got.Metadata.DisplayName != "jacinta's phone" {
		t.Fatalf("registry entry: got %+v ok=%v", got, ok)
	}
}

// TestPairingCodeSingleUse: approving twice is refused.
func TestPairingCodeSingleUse(t *testing.T) {
	clock, _ := fakeClock(time.Unix(0, 0))
	reg := NewRegistry(clock)
	pm := NewPairingManager(clock, reg)
	code, _ := pm.Issue(time.Minute)
	deviceKey := mustKey(t)
	if err := pm.Claim(code.Code, deviceKey.PublicBytes(), DeviceMetadata{}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := pm.Approve(code.Code, nil); err != nil {
		t.Fatalf("first Approve: %v", err)
	}
	if _, err := pm.Approve(code.Code, nil); !errors.Is(err, ErrPairingCodeUsed) {
		t.Fatalf("second Approve: got %v want ErrPairingCodeUsed", err)
	}
	if err := pm.Claim(code.Code, deviceKey.PublicBytes(), DeviceMetadata{}); !errors.Is(err, ErrPairingCodeUsed) {
		t.Fatalf("Claim after Approve: got %v want ErrPairingCodeUsed", err)
	}
}

// TestPairingCodeExpires: a code past its TTL cannot be claimed or
// approved.
func TestPairingCodeExpires(t *testing.T) {
	clock, advance := fakeClock(time.Unix(0, 0))
	reg := NewRegistry(clock)
	pm := NewPairingManager(clock, reg)
	code, _ := pm.Issue(time.Minute)
	advance(2 * time.Minute)

	deviceKey := mustKey(t)
	if err := pm.Claim(code.Code, deviceKey.PublicBytes(), DeviceMetadata{}); !errors.Is(err, ErrPairingCodeExpired) {
		t.Fatalf("Claim after expiry: got %v want ErrPairingCodeExpired", err)
	}
	if pm.Status(code.Code) != StatusExpired {
		t.Fatalf("Status after expiry: got %q want expired", pm.Status(code.Code))
	}
}

// TestPairingApproveBeforeClaimRefused: nothing to approve until a device
// has claimed the code.
func TestPairingApproveBeforeClaimRefused(t *testing.T) {
	clock, _ := fakeClock(time.Unix(0, 0))
	reg := NewRegistry(clock)
	pm := NewPairingManager(clock, reg)
	code, _ := pm.Issue(time.Minute)
	if _, err := pm.Approve(code.Code, nil); !errors.Is(err, ErrPairingNotClaimed) {
		t.Fatalf("Approve before Claim: got %v want ErrPairingNotClaimed", err)
	}
}

// TestPairingUnknownCode covers Claim/Approve/Status on a code the manager
// never issued.
func TestPairingUnknownCode(t *testing.T) {
	clock, _ := fakeClock(time.Unix(0, 0))
	reg := NewRegistry(clock)
	pm := NewPairingManager(clock, reg)
	deviceKey := mustKey(t)
	if err := pm.Claim("bogus", deviceKey.PublicBytes(), DeviceMetadata{}); !errors.Is(err, ErrPairingCodeUnknown) {
		t.Fatalf("Claim unknown code: got %v want ErrPairingCodeUnknown", err)
	}
	if _, err := pm.Approve("bogus", nil); !errors.Is(err, ErrPairingCodeUnknown) {
		t.Fatalf("Approve unknown code: got %v want ErrPairingCodeUnknown", err)
	}
	if pm.Status("bogus") != StatusExpired {
		t.Fatalf("Status unknown code: got %q want expired (never issued reads the same as gone)", pm.Status("bogus"))
	}
}

// TestPairingExplicitCapabilitiesOverrideDefault: Approve honors a
// caller-supplied capability set instead of the default when one is given
// (spec §4: "upgrade explicit").
func TestPairingExplicitCapabilitiesOverrideDefault(t *testing.T) {
	clock, _ := fakeClock(time.Unix(0, 0))
	reg := NewRegistry(clock)
	pm := NewPairingManager(clock, reg)
	code, _ := pm.Issue(time.Minute)
	deviceKey := mustKey(t)
	if err := pm.Claim(code.Code, deviceKey.PublicBytes(), DeviceMetadata{}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	admin := []contracts.Capability{contracts.CapAdmin, contracts.CapInteractive, contracts.CapApprover, contracts.CapObserver}
	d, err := pm.Approve(code.Code, admin)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !contracts.Holds(d.Capabilities, contracts.CapAdmin) {
		t.Fatalf("explicit admin grant not applied: got %v", d.Capabilities)
	}
}
