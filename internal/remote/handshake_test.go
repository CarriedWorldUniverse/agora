package remote

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func mustKey(t *testing.T) StaticKey {
	t.Helper()
	k, err := GenerateStaticKey()
	if err != nil {
		t.Fatalf("GenerateStaticKey: %v", err)
	}
	return k
}

// TestHandshakeEnrollmentMatrix is the DoD's table: the IK handshake
// succeeds only for an enrolled, non-revoked device.
func TestHandshakeEnrollmentMatrix(t *testing.T) {
	tests := []struct {
		name      string
		enroll    bool
		revoke    bool
		wantOK    bool
		wantErrIs error
	}{
		{name: "unenrolled device refused", enroll: false, wantOK: false, wantErrIs: ErrDeviceNotEnrolled},
		{name: "enrolled non-revoked device accepted", enroll: true, revoke: false, wantOK: true},
		{name: "enrolled then revoked device refused", enroll: true, revoke: true, wantOK: false, wantErrIs: ErrDeviceRevoked},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			daemonKey := mustKey(t)
			deviceKey := mustKey(t)
			reg := NewRegistry(func() time.Time { return time.Unix(0, 0) })

			fp := Fingerprint(deviceKey.PublicBytes())
			if tc.enroll {
				if _, err := reg.Enroll(fp, deviceKey.PublicBytes(), DeviceMetadata{DisplayName: "phone"}, DefaultCapabilities()); err != nil {
					t.Fatalf("Enroll: %v", err)
				}
			}
			if tc.revoke {
				if err := reg.Revoke(fp); err != nil {
					t.Fatalf("Revoke: %v", err)
				}
			}

			prologue := Prologue{DaemonID: "daemon1", StreamID: "s1", Epoch: 1}
			init, err := NewInitiatorHandshake(deviceKey, daemonKey.Public(), prologue)
			if err != nil {
				t.Fatalf("NewInitiatorHandshake: %v", err)
			}
			m1, err := init.Message1([]byte("session-token"))
			if err != nil {
				t.Fatalf("Message1: %v", err)
			}

			// Responder's prologue must carry the SAME claimed device id
			// the initiator bound (out-of-band, e.g. an attach request's
			// client_id) — Accept cross-checks it against what the crypto
			// actually recovers.
			responderPrologue := prologue
			responderPrologue.DeviceID = fp
			resp := NewResponderHandshake(daemonKey, responderPrologue, reg)
			deviceFP, token, m2, tk, err := resp.Accept(m1)

			if tc.wantOK {
				if err != nil {
					t.Fatalf("Accept: unexpected error: %v", err)
				}
				if deviceFP != fp {
					t.Errorf("deviceFP: got %q want %q", deviceFP, fp)
				}
				if string(token) != "session-token" {
					t.Errorf("token: got %q", token)
				}
				finalTK, err := init.Complete(m2)
				if err != nil {
					t.Fatalf("initiator Complete: %v", err)
				}
				if !bytes.Equal(finalTK.InitiatorToResponder, tk.InitiatorToResponder) {
					t.Errorf("initiator->responder key mismatch between sides")
				}
				if !bytes.Equal(finalTK.ResponderToInitiator, tk.ResponderToInitiator) {
					t.Errorf("responder->initiator key mismatch between sides")
				}
				return
			}

			if err == nil {
				t.Fatalf("Accept: expected refusal, got success (deviceFP=%q)", deviceFP)
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Errorf("error: got %v want errors.Is(%v)", err, tc.wantErrIs)
			}
			if m2.Ephemeral != nil || m2.EncConfirm != nil {
				t.Errorf("refused handshake must not produce a Message2")
			}
		})
	}
}

// TestHandshakeRefusesPrologueMismatch: a claimed device_id that doesn't
// match the identity the crypto actually recovers is refused — this is
// the cross-binding/replay protection (spec §2).
func TestHandshakeRefusesPrologueMismatch(t *testing.T) {
	daemonKey := mustKey(t)
	deviceKey := mustKey(t)
	reg := NewRegistry(nil)
	fp := Fingerprint(deviceKey.PublicBytes())
	if _, err := reg.Enroll(fp, deviceKey.PublicBytes(), DeviceMetadata{}, DefaultCapabilities()); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	prologue := Prologue{DaemonID: "daemon1", StreamID: "s1", Epoch: 1}
	init, err := NewInitiatorHandshake(deviceKey, daemonKey.Public(), prologue)
	if err != nil {
		t.Fatalf("NewInitiatorHandshake: %v", err)
	}
	m1, err := init.Message1([]byte("tok"))
	if err != nil {
		t.Fatalf("Message1: %v", err)
	}

	// Responder expects a DIFFERENT device id than what the crypto recovers.
	badPrologue := prologue
	badPrologue.DeviceID = "agora:someone-else"
	resp := NewResponderHandshake(daemonKey, badPrologue, reg)
	_, _, _, _, err = resp.Accept(m1)
	if !errors.Is(err, ErrHandshakeFailed) {
		t.Fatalf("Accept: got %v want ErrHandshakeFailed (prologue mismatch)", err)
	}
}

// TestHandshakeRefusesTamperedMessage: bit-flipping any ciphertext field
// must not be silently accepted (AEAD integrity).
func TestHandshakeRefusesTamperedMessage(t *testing.T) {
	daemonKey := mustKey(t)
	deviceKey := mustKey(t)
	reg := NewRegistry(nil)
	fp := Fingerprint(deviceKey.PublicBytes())
	if _, err := reg.Enroll(fp, deviceKey.PublicBytes(), DeviceMetadata{}, DefaultCapabilities()); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	prologue := Prologue{DaemonID: "daemon1", StreamID: "s1", Epoch: 1, DeviceID: fp}
	init, err := NewInitiatorHandshake(deviceKey, daemonKey.Public(), prologue)
	if err != nil {
		t.Fatalf("NewInitiatorHandshake: %v", err)
	}
	m1, err := init.Message1([]byte("tok"))
	if err != nil {
		t.Fatalf("Message1: %v", err)
	}
	m1.EncStatic[0] ^= 0xFF

	resp := NewResponderHandshake(daemonKey, prologue, reg)
	_, _, _, _, err = resp.Accept(m1)
	if !errors.Is(err, ErrHandshakeFailed) {
		t.Fatalf("Accept: got %v want ErrHandshakeFailed (tampered ciphertext)", err)
	}
}

// TestHandshakeReEnrollAfterRevokeRequiresExplicitUnrevoke: Enroll refuses
// to silently resurrect a revoked device id (see registry.go doc comment).
func TestHandshakeReEnrollAfterRevokeRequiresExplicitUnrevoke(t *testing.T) {
	reg := NewRegistry(nil)
	deviceKey := mustKey(t)
	fp := Fingerprint(deviceKey.PublicBytes())
	if _, err := reg.Enroll(fp, deviceKey.PublicBytes(), DeviceMetadata{}, DefaultCapabilities()); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := reg.Revoke(fp); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := reg.Enroll(fp, deviceKey.PublicBytes(), DeviceMetadata{}, DefaultCapabilities()); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("re-Enroll over revoked id: got %v want ErrDeviceRevoked", err)
	}
	if err := reg.Unrevoke(fp); err != nil {
		t.Fatalf("Unrevoke: %v", err)
	}
	if _, err := reg.Enroll(fp, deviceKey.PublicBytes(), DeviceMetadata{}, DefaultCapabilities()); err != nil {
		t.Fatalf("re-Enroll after Unrevoke: %v", err)
	}
}

// TestFingerprintDeterministic: Fingerprint is a pure function of the
// public key bytes (needed for the responder to recompute exactly what
// the initiator claims).
func TestFingerprintDeterministic(t *testing.T) {
	k := mustKey(t)
	a := Fingerprint(k.PublicBytes())
	b := Fingerprint(k.PublicBytes())
	if a != b {
		t.Fatalf("Fingerprint not deterministic: %q vs %q", a, b)
	}
	other := mustKey(t)
	if Fingerprint(other.PublicBytes()) == a {
		t.Fatalf("distinct keys produced the same fingerprint")
	}
	_ = contracts.IdentityDevice // documents the shared identity-kind link
}
