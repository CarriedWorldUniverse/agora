// Pairing implements the passkey-style enrollment ceremony (spec §3),
// codex System A's UX with agora's trust model: a short-TTL single-use
// code, a device claim carrying its static key, and an explicit operator
// approval before the Registry ever gains a new entry.
package remote

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// pairingCodeBytes sizes the random code (before base32 encoding) —
// generous enough that guessing within the TTL is infeasible.
const pairingCodeBytes = 10

// PairingCode is what step 1 hands back to the trusted (already-logged-in)
// surface: display as QR + short string (spec §3 step 1).
type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

// pairingState is the ceremony's tiny state machine: pending (issued, not
// yet claimed) -> claimed (device presented its key, awaiting operator
// approval) -> approved (Registry entry created; code single-use, now
// dead) or expired.
type pairingState int

const (
	pendingIssued pairingState = iota
	pendingClaimed
	pendingApproved
)

type pendingPairing struct {
	code      string
	expiresAt time.Time
	state     pairingState

	// set once claimed (spec §3 step 2): "the device's static public key +
	// metadata".
	deviceStaticPub []byte
	metadata        DeviceMetadata
}

// PairingStatus is what a claiming device polls (spec §3 step 3: "poll
// pairing/status → {claimed}").
type PairingStatus string

const (
	StatusPending  PairingStatus = "pending"
	StatusClaimed  PairingStatus = "claimed"
	StatusApproved PairingStatus = "approved"
	StatusExpired  PairingStatus = "expired"
)

// PairingManager runs the ceremony against a Registry: Issue, Claim,
// Approve, Status. Safe for concurrent use.
type PairingManager struct {
	clock Clock
	reg   *Registry

	mu      sync.Mutex
	pending map[string]*pendingPairing
}

// NewPairingManager builds a manager writing approved enrollments into reg.
func NewPairingManager(clock Clock, reg *Registry) *PairingManager {
	if clock == nil {
		clock = time.Now
	}
	return &PairingManager{clock: clock, reg: reg, pending: make(map[string]*pendingPairing)}
}

// Issue starts a new pairing ceremony with the given TTL (spec §3 step 1).
func (m *PairingManager) Issue(ttl time.Duration) (PairingCode, error) {
	raw := make([]byte, pairingCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return PairingCode{}, fmt.Errorf("remote: pairing code: %w", err)
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	expires := m.clock().Add(ttl)
	m.mu.Lock()
	m.pending[code] = &pendingPairing{code: code, expiresAt: expires, state: pendingIssued}
	m.mu.Unlock()
	return PairingCode{Code: code, ExpiresAt: expires}, nil
}

// Claim records a device's presentation of its static key + metadata
// against an issued code (spec §3 step 2). Refuses an unknown, expired, or
// already-claimed/approved code.
func (m *PairingManager) Claim(code string, staticPub []byte, meta DeviceMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[code]
	if !ok {
		return ErrPairingCodeUnknown
	}
	if m.clock().After(p.expiresAt) {
		delete(m.pending, code)
		return ErrPairingCodeExpired
	}
	if p.state != pendingIssued {
		return ErrPairingCodeUsed
	}
	p.deviceStaticPub = append([]byte(nil), staticPub...)
	p.metadata = meta
	p.state = pendingClaimed
	return nil
}

// Status reports the ceremony's current state for the polling device (spec
// §3 step 3).
func (m *PairingManager) Status(code string) PairingStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[code]
	if !ok {
		return StatusExpired
	}
	if m.clock().After(p.expiresAt) && p.state != pendingApproved {
		return StatusExpired
	}
	switch p.state {
	case pendingClaimed:
		return StatusClaimed
	case pendingApproved:
		return StatusApproved
	default:
		return StatusPending
	}
}

// Approve is the passkey ceremony's attestation moment (spec §3 step 3):
// the operator, on an already-trusted surface, confirms the claiming
// device's name+fingerprint and the code is consumed into a Registry
// enrollment with caps (nil ⇒ DefaultCapabilities()). Single-use: a second
// Approve on the same code fails with ErrPairingCodeUsed, and an
// unclaimed/expired code cannot be approved at all.
func (m *PairingManager) Approve(code string, caps []contracts.Capability) (Device, error) {
	m.mu.Lock()
	p, ok := m.pending[code]
	if !ok {
		m.mu.Unlock()
		return Device{}, ErrPairingCodeUnknown
	}
	if m.clock().After(p.expiresAt) {
		delete(m.pending, code)
		m.mu.Unlock()
		return Device{}, ErrPairingCodeExpired
	}
	switch p.state {
	case pendingIssued:
		m.mu.Unlock()
		return Device{}, ErrPairingNotClaimed
	case pendingApproved:
		m.mu.Unlock()
		return Device{}, ErrPairingCodeUsed
	}
	staticPub := p.deviceStaticPub
	meta := p.metadata
	p.state = pendingApproved
	m.mu.Unlock()

	if caps == nil {
		caps = DefaultCapabilities()
	}
	fp := Fingerprint(staticPub)
	return m.reg.Enroll(fp, staticPub, meta, caps)
}
