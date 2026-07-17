package remote

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
)

// StaticKey is one end's Noise IK static keypair — "static keys = identity,
// literally the identity primitive" (spec §2). Device keys are
// passkey-style: generated on-device, private key never leaves it (secure
// enclave where offered; file fallback 0600 is the caller's persistence
// concern, not this package's — StaticKey only holds the in-memory key
// material and never serializes the private half itself).
//
// Curve: X25519 via crypto/ecdh (stdlib) — the DH primitive
// Noise_IK_25519_AESGCM_SHA256 names. No hand-rolled elliptic-curve math.
type StaticKey struct {
	private *ecdh.PrivateKey
}

// GenerateStaticKey creates a fresh X25519 static keypair using
// crypto/rand — never math/rand, never a derived/predictable seed for a
// live device (ground rule 6).
func GenerateStaticKey() (StaticKey, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return StaticKey{}, fmt.Errorf("remote: generate static key: %w", err)
	}
	return StaticKey{private: priv}, nil
}

// Public returns the public half, safe to publish (pairing claim,
// enrollment record, pinned by the peer).
func (k StaticKey) Public() *ecdh.PublicKey {
	if k.private == nil {
		return nil
	}
	return k.private.PublicKey()
}

// PublicBytes returns the raw public key bytes (what rides on the wire in a
// pairing claim and what Fingerprint hashes).
func (k StaticKey) PublicBytes() []byte {
	pub := k.Public()
	if pub == nil {
		return nil
	}
	return pub.Bytes()
}

// ParsePublicKey decodes raw X25519 public key bytes received over the
// wire (a pairing claim, a pinned daemon key). Returns an error for
// malformed/wrong-length/low-order input — crypto/ecdh validates this so
// this package doesn't have to (no hand-rolled point validation).
func ParsePublicKey(raw []byte) (*ecdh.PublicKey, error) {
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("remote: parse device public key: %w", err)
	}
	return pub, nil
}

// DH performs the X25519 Diffie-Hellman step: priv (this key) with peer's
// public key. Returns an error for a degenerate/all-zero shared secret
// (crypto/ecdh rejects these) — never silently proceeds with a weak key.
func (k StaticKey) DH(peer *ecdh.PublicKey) ([]byte, error) {
	if k.private == nil || peer == nil {
		return nil, fmt.Errorf("remote: DH: missing key material")
	}
	shared, err := k.private.ECDH(peer)
	if err != nil {
		return nil, fmt.Errorf("remote: DH: %w", err)
	}
	return shared, nil
}

// fingerprintPrefix is the identity-primitive prefix contracts/identity.go
// documents: "agora:<base32(sha256(pubkey))[..16]>".
const fingerprintPrefix = "agora:"

// fingerprintLen is the truncated base32 length the shared scheme uses.
const fingerprintLen = 16

// Fingerprint computes the authoritative device/daemon identity id from raw
// public key bytes: agora:<base32(sha256(pubkey))[..16]>. Same scheme as
// every other identity kind (contracts.Identity) — remote control is just
// the device-identity binding of the one keypair-is-identity model.
func Fingerprint(pubKeyBytes []byte) string {
	sum := sha256.Sum256(pubKeyBytes)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	if len(enc) > fingerprintLen {
		enc = enc[:fingerprintLen]
	}
	return fingerprintPrefix + enc
}
