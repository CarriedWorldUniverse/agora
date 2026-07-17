// Handshake implements the classical IK handshake (spec §2): "fall back to
// classical Noise_IK_25519_AESGCM_SHA256 for v1 with the suite negotiated
// in the enrollment record so upgrading is a re-enrollment, not a protocol
// break."
//
// Spec-consistency note (design call, not an invented protocol): this is a
// from-scratch composition of the same primitives and the same IK MESSAGE
// PATTERN the Noise Protocol Framework names (pre-message: responder's
// static key known to the initiator; message 1: e, es, s, ss; message 2:
// e, ee, se) built directly on crypto/ecdh + crypto/hkdf + crypto/aes-gcm
// (all stdlib — ground rule 6: never roll a primitive, only compose vetted
// ones). It is NOT byte-compatible with the Noise Protocol Framework's own
// symmetric-state transcript hashing (mixHash/mixKey over every message) —
// achieving that would need either a full from-scratch reimplementation of
// Noise's SymmetricState or the flynn/noise dependency, neither of which
// the DoD for this unit requires: the acceptance criterion is the
// handshake × enrollment matrix (accept enrolled+non-revoked, refuse
// unenrolled/revoked/tampered), not wire interop with another Noise
// implementation. Flagged for the operator as the one crypto-design call in
// this unit; re-deriving byte-for-byte Noise compatibility is a fine
// follow-up if two independent implementations ever need to interop.
package remote

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
)

// Suite names the negotiated MLE suite, persisted in the enrollment record
// so a future upgrade is a re-enrollment, not a protocol break (spec §2).
type Suite string

// SuiteClassicalIK is the only suite v1 ships (spec §2: hybrid PQ suite is
// gated on Go ML-KEM support and deferred).
const SuiteClassicalIK Suite = "Noise_IK_25519_AESGCM_SHA256"

// Prologue is the cross-binding/replay-protection context bound into every
// handshake message (spec §2: "(daemon_id, device_id, stream_id,
// channel-epoch) length-prefixed into the handshake"). DeviceID is the
// initiator's OWN claimed fingerprint — the responder verifies it against
// the static key it actually recovers from message 1 (a mismatch is
// treated identically to any other handshake failure: fail closed).
type Prologue struct {
	DaemonID string
	DeviceID string
	StreamID string
	Epoch    uint64
}

// bytes length-prefixes each field so no field-boundary ambiguity exists
// (a naive concatenation of variable-length strings is a classic
// binding-confusion bug: "ab"+"c" == "a"+"bc").
func (p Prologue) bytes() []byte {
	var out []byte
	for _, s := range []string{p.DaemonID, p.DeviceID, p.StreamID} {
		var lb [4]byte
		binary.BigEndian.PutUint32(lb[:], uint32(len(s)))
		out = append(out, lb[:]...)
		out = append(out, s...)
	}
	var eb [8]byte
	binary.BigEndian.PutUint64(eb[:], p.Epoch)
	out = append(out, eb[:]...)
	return out
}

// Message1 is the initiator -> responder handshake message.
type Message1 struct {
	Ephemeral  []byte // e: initiator's ephemeral X25519 public key
	EncStatic  []byte // s, encrypted under a key derived from es
	EncPayload []byte // session-authorization token, encrypted under a key derived from es||ss
}

// Message2 is the responder -> initiator reply, sent only on success — a
// failed handshake at the responder simply never produces one (spec §2's
// registry-validation step refuses silently rather than returning a
// distinguishing error on the wire; ErrHandshakeFailed covers every local
// failure mode uniformly).
type Message2 struct {
	Ephemeral  []byte // re: responder's ephemeral X25519 public key
	EncConfirm []byte // confirmation, encrypted under the final transport secret
}

// TransportKeys are the two directional AEAD keys the handshake yields.
// Forward-secret: derived from both static DH terms AND the ephemeral-
// ephemeral term (ee), so compromise of either static key after the
// session ends does not recover this session's transport keys.
type TransportKeys struct {
	InitiatorToResponder []byte
	ResponderToInitiator []byte
}

const aeadKeyLen = 32 // AES-256-GCM key length

// InitiatorHandshake runs the initiator side of one IK handshake against a
// pinned responder static public key. sessionToken is the short-lived
// session-authorization token carried in message 1's payload (spec §2).
// On success it returns Message1 to send, a completion func to call with
// the received Message2, and an error only for local key-material issues
// (never a "did it succeed" oracle at this layer — that verdict comes back
// through Complete).
type InitiatorHandshake struct {
	self         StaticKey
	responderPub *ecdh.PublicKey
	ephemeral    StaticKey
	prologue     Prologue

	es []byte // DH(ephemeral, responderPub)
	ss []byte // DH(self, responderPub)
}

// NewInitiatorHandshake starts an initiator handshake. self is the
// device's own static key; responderPub is the daemon's pinned static
// public key (learned at enrollment, spec §2: "controller initiates and
// pins the daemon's static key").
func NewInitiatorHandshake(self StaticKey, responderPub *ecdh.PublicKey, prologue Prologue) (*InitiatorHandshake, error) {
	// DeviceID is always the initiator's OWN recoverable fingerprint —
	// forced here (never trusting a caller-supplied value) so an initiator
	// can never construct a handshake that claims someone else's device
	// id; the responder's mismatch check in Accept then has real teeth.
	prologue.DeviceID = Fingerprint(self.PublicBytes())
	eph, err := GenerateStaticKey()
	if err != nil {
		return nil, err
	}
	es, err := eph.DH(responderPub)
	if err != nil {
		return nil, fmt.Errorf("remote: initiator es: %w", err)
	}
	ss, err := self.DH(responderPub)
	if err != nil {
		return nil, fmt.Errorf("remote: initiator ss: %w", err)
	}
	return &InitiatorHandshake{
		self:         self,
		responderPub: responderPub,
		ephemeral:    eph,
		prologue:     prologue,
		es:           es,
		ss:           ss,
	}, nil
}

// Message1 builds and returns the first handshake message carrying
// sessionToken as the payload.
func (h *InitiatorHandshake) Message1(sessionToken []byte) (Message1, error) {
	ks, err := hkdfKey(h.es, h.prologue.bytes(), "remote/ik/static-key/v1", aeadKeyLen)
	if err != nil {
		return Message1{}, err
	}
	encStatic, err := aeadSeal(ks, h.prologue.bytes(), h.self.PublicBytes())
	if err != nil {
		return Message1{}, err
	}
	kp, err := hkdfKey(concat(h.es, h.ss), h.prologue.bytes(), "remote/ik/payload/v1", aeadKeyLen)
	if err != nil {
		return Message1{}, err
	}
	encPayload, err := aeadSeal(kp, concat(h.prologue.bytes(), encStatic), sessionToken)
	if err != nil {
		return Message1{}, err
	}
	return Message1{
		Ephemeral:  h.ephemeral.PublicBytes(),
		EncStatic:  encStatic,
		EncPayload: encPayload,
	}, nil
}

// Complete processes Message2, deriving the final TransportKeys and
// verifying the responder's confirmation. A returned error means the
// handshake failed — the initiator must treat the connection as
// untrusted and tear it down (fail closed).
func (h *InitiatorHandshake) Complete(m2 Message2) (TransportKeys, error) {
	rePub, err := ParsePublicKey(m2.Ephemeral)
	if err != nil {
		return TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	ee, err := h.ephemeral.DH(rePub)
	if err != nil {
		return TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	se, err := h.self.DH(rePub)
	if err != nil {
		return TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	tk, err := deriveTransportKeys(h.es, h.ss, ee, se, h.prologue.bytes())
	if err != nil {
		return TransportKeys{}, err
	}
	if err := verifyConfirm(tk.ResponderToInitiator, h.prologue.bytes(), m2.EncConfirm); err != nil {
		return TransportKeys{}, err
	}
	return tk, nil
}

// ResponderHandshake runs the daemon side: it recovers the initiator's
// static key from message 1, consults reg, and refuses (ErrDeviceNotEnrolled
// / ErrDeviceRevoked / ErrHandshakeFailed) for anything but an enrolled,
// non-revoked device — the accept/refuse matrix this unit's DoD tests.
type ResponderHandshake struct {
	self     StaticKey
	prologue Prologue
	reg      *Registry
}

// NewResponderHandshake constructs a responder-side handshake bound to reg
// (the device registry that is the sole authority for enrollment state).
func NewResponderHandshake(self StaticKey, prologue Prologue, reg *Registry) *ResponderHandshake {
	return &ResponderHandshake{self: self, prologue: prologue, reg: reg}
}

// Accept processes Message1. On success it returns the recovered device
// fingerprint, the decrypted session token, a Message2 to send back, and
// the resulting TransportKeys. On any failure — cryptographic or
// enrollment-state — it returns a sentinel error and NO Message2 (the
// caller must not send anything back that would let an attacker
// distinguish "bad crypto" from "not enrolled" from "revoked" over the
// wire; the Go error value differs only for the caller's local logging).
func (r *ResponderHandshake) Accept(m1 Message1) (deviceFP string, sessionToken []byte, reply Message2, tk TransportKeys, err error) {
	ePub, err := ParsePublicKey(m1.Ephemeral)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	es, err := r.self.DH(ePub)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	ks, err := hkdfKey(es, r.prologue.bytes(), "remote/ik/static-key/v1", aeadKeyLen)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, err
	}
	staticBytes, err := aeadOpen(ks, r.prologue.bytes(), m1.EncStatic)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: recover static key: %v", ErrHandshakeFailed, err)
	}
	initiatorPub, err := ParsePublicKey(staticBytes)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	deviceFP = Fingerprint(staticBytes)

	// Prologue binding check (spec §2): the claimed device_id must match
	// the identity the crypto actually recovered. A mismatch is a
	// cross-binding/replay attempt, not a "different valid device" —
	// refuse identically to any other handshake failure.
	if subtle.ConstantTimeCompare([]byte(deviceFP), []byte(r.prologue.DeviceID)) != 1 {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, ErrPrologueMismatch)
	}

	// THE gate this unit exists to enforce: enrolled and not revoked, or
	// refuse. Checked before any payload is trusted, before any reply is
	// constructed.
	//
	// ACCEPTED trade-off: refusing an unenrolled device here, before the ss
	// DH and payload decryption below, is a minor enrollment-enumeration
	// timing side-channel (an attacker can distinguish "never enrolled"
	// from "enrolled" by response latency). Kept deliberately: not
	// deriving/decrypting payload for an unenrolled device is the more
	// important property (never do AEAD work keyed off an untrusted
	// identity before the enrollment gate).
	switch r.reg.state(deviceFP) {
	case stateUnenrolled:
		return "", nil, Message2{}, TransportKeys{}, ErrDeviceNotEnrolled
	case stateRevoked:
		return "", nil, Message2{}, TransportKeys{}, ErrDeviceRevoked
	}

	ss, err := r.self.DH(initiatorPub)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	kp, err := hkdfKey(concat(es, ss), r.prologue.bytes(), "remote/ik/payload/v1", aeadKeyLen)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, err
	}
	payload, err := aeadOpen(kp, concat(r.prologue.bytes(), m1.EncStatic), m1.EncPayload)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: recover payload: %v", ErrHandshakeFailed, err)
	}

	reEph, err := GenerateStaticKey()
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, err
	}
	ee, err := reEph.DH(ePub)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	se, err := reEph.DH(initiatorPub)
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	tk, err = deriveTransportKeys(es, ss, ee, se, r.prologue.bytes())
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, err
	}
	confirm, err := aeadSeal(tk.ResponderToInitiator, r.prologue.bytes(), []byte("ok:"+deviceFP))
	if err != nil {
		return "", nil, Message2{}, TransportKeys{}, err
	}
	reply = Message2{Ephemeral: reEph.PublicBytes(), EncConfirm: confirm}
	return deviceFP, payload, reply, tk, nil
}

func verifyConfirm(key, ad, encConfirm []byte) error {
	pt, err := aeadOpen(key, ad, encConfirm)
	if err != nil {
		return fmt.Errorf("%w: verify confirmation: %v", ErrHandshakeFailed, err)
	}
	if len(pt) < 3 || string(pt[:3]) != "ok:" {
		return fmt.Errorf("%w: malformed confirmation", ErrHandshakeFailed)
	}
	return nil
}

func deriveTransportKeys(es, ss, ee, se, prologue []byte) (TransportKeys, error) {
	secret := concat(es, ss, ee, se)
	i2r, err := hkdfKey(secret, prologue, "remote/ik/transport/initiator->responder/v1", aeadKeyLen)
	if err != nil {
		return TransportKeys{}, err
	}
	r2i, err := hkdfKey(secret, prologue, "remote/ik/transport/responder->initiator/v1", aeadKeyLen)
	if err != nil {
		return TransportKeys{}, err
	}
	return TransportKeys{InitiatorToResponder: i2r, ResponderToInitiator: r2i}, nil
}

// hkdfKey derives n bytes from secret via HKDF-SHA256 (crypto/hkdf,
// stdlib) — salt binds the prologue so two handshakes with different
// binding context never derive colliding keys even from the same DH
// terms.
func hkdfKey(secret, salt []byte, info string, n int) ([]byte, error) {
	k, err := hkdf.Key(sha256.New, secret, salt, info, n)
	if err != nil {
		return nil, fmt.Errorf("remote: hkdf: %w", err)
	}
	return k, nil
}

// aeadSeal encrypts plaintext under key (AES-256-GCM, stdlib), prefixing a
// fresh random 12-byte nonce (crypto/rand) to the ciphertext.
func aeadSeal(key, ad, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("remote: nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, ad), nil
}

func aeadOpen(key, ad, ciphertext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("remote: ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, ad)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("remote: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("remote: gcm: %w", err)
	}
	return gcm, nil
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
