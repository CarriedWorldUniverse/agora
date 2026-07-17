package remote

import (
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestRegistryDeviceManagement: list/revoke/last_seen_at (spec §3: "Device
// management: device list / device revoke ... last_seen_at tracking").
func TestRegistryDeviceManagement(t *testing.T) {
	clock, advance := fakeClock(time.Unix(1000, 0))
	reg := NewRegistry(clock)

	k1, k2 := mustKey(t), mustKey(t)
	fp1, fp2 := Fingerprint(k1.PublicBytes()), Fingerprint(k2.PublicBytes())
	if _, err := reg.Enroll(fp1, k1.PublicBytes(), DeviceMetadata{DisplayName: "phone"}, DefaultCapabilities()); err != nil {
		t.Fatalf("Enroll fp1: %v", err)
	}
	if _, err := reg.Enroll(fp2, k2.PublicBytes(), DeviceMetadata{DisplayName: "laptop"}, DefaultCapabilities()); err != nil {
		t.Fatalf("Enroll fp2: %v", err)
	}

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("List: got %d devices, want 2", len(list))
	}

	advance(time.Hour)
	reg.Touch(fp1)
	d1, _ := reg.Get(fp1)
	if !d1.LastSeenAt.Equal(clock()) {
		t.Errorf("Touch: last_seen_at got %v want %v", d1.LastSeenAt, clock())
	}

	if err := reg.Revoke(fp2); err != nil {
		t.Fatalf("Revoke fp2: %v", err)
	}
	d2, _ := reg.Get(fp2)
	if !d2.Revoked {
		t.Errorf("fp2 should be revoked")
	}
	// Revoked devices still show up in List (full history, not hidden).
	if list := reg.List(); len(list) != 2 {
		t.Fatalf("List after Revoke: got %d devices, want 2 (revoked stays visible)", len(list))
	}

	if err := reg.Revoke("nope"); !errors.Is(err, ErrDeviceNotEnrolled) {
		t.Fatalf("Revoke unknown id: got %v want ErrDeviceNotEnrolled", err)
	}
}

// TestRegistrySetCapabilitiesUpgradeExplicit: an existing device's
// capability set can be widened only by an explicit call, never
// implicitly.
func TestRegistrySetCapabilitiesUpgradeExplicit(t *testing.T) {
	reg := NewRegistry(nil)
	k := mustKey(t)
	fp := Fingerprint(k.PublicBytes())
	if _, err := reg.Enroll(fp, k.PublicBytes(), DeviceMetadata{}, DefaultCapabilities()); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	d, _ := reg.Get(fp)
	if contracts.Holds(d.Capabilities, contracts.CapAdmin) {
		t.Fatalf("new device should not default to admin")
	}
	if err := reg.SetCapabilities(fp, []contracts.Capability{contracts.CapAdmin}); err != nil {
		t.Fatalf("SetCapabilities: %v", err)
	}
	d, _ = reg.Get(fp)
	if !contracts.Holds(d.Capabilities, contracts.CapAdmin) {
		t.Fatalf("SetCapabilities did not take effect: %v", d.Capabilities)
	}
}

// TestRegistrySetConstraints.
func TestRegistrySetConstraints(t *testing.T) {
	reg := NewRegistry(nil)
	k := mustKey(t)
	fp := Fingerprint(k.PublicBytes())
	if _, err := reg.Enroll(fp, k.PublicBytes(), DeviceMetadata{}, DefaultCapabilities()); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	c := DeviceConstraints{AllowedProfiles: []string{"chat"}}
	if err := reg.SetConstraints(fp, c); err != nil {
		t.Fatalf("SetConstraints: %v", err)
	}
	d, _ := reg.Get(fp)
	if len(d.Constraints.AllowedProfiles) != 1 || d.Constraints.AllowedProfiles[0] != "chat" {
		t.Fatalf("constraints not applied: %+v", d.Constraints)
	}
}

// TestRegistryUnknownDeviceOperationsFailClosed: every mutating op on an
// id the registry has never enrolled refuses (fail closed).
func TestRegistryUnknownDeviceOperationsFailClosed(t *testing.T) {
	reg := NewRegistry(nil)
	if err := reg.SetCapabilities("ghost", []contracts.Capability{contracts.CapAdmin}); !errors.Is(err, ErrDeviceNotEnrolled) {
		t.Errorf("SetCapabilities on unknown id: got %v want ErrDeviceNotEnrolled", err)
	}
	if err := reg.SetConstraints("ghost", DeviceConstraints{}); !errors.Is(err, ErrDeviceNotEnrolled) {
		t.Errorf("SetConstraints on unknown id: got %v want ErrDeviceNotEnrolled", err)
	}
	if err := reg.Unrevoke("ghost"); !errors.Is(err, ErrDeviceNotEnrolled) {
		t.Errorf("Unrevoke on unknown id: got %v want ErrDeviceNotEnrolled", err)
	}
	if _, ok := reg.Get("ghost"); ok {
		t.Errorf("Get on unknown id: ok should be false")
	}
}
