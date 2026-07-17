package remote

import (
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Clock returns the current time. Injected everywhere this package would
// otherwise call time.Now(), so timeout/expiry tests are deterministic
// (ground rule 4: no wall-clock).
type Clock func() time.Time

// DeviceMetadata is the claim payload a pairing enrollment records (spec
// §3 step 2): "{display_name, device_type, platform, os_version,
// app_version}".
type DeviceMetadata struct {
	DisplayName string `json:"display_name"`
	DeviceType  string `json:"device_type,omitempty"`
	Platform    string `json:"platform,omitempty"`
	OSVersion   string `json:"os_version,omitempty"`
	AppVersion  string `json:"app_version,omitempty"`
	// KeyCustody records softer-than-hardware key storage (spec §3a:
	// "software" for the WASM/WebCrypto-AES fallback). Empty means
	// hardware/OS-keychain-equivalent custody — the default assumption for
	// non-browser devices.
	KeyCustody string `json:"key_custody,omitempty"`
}

// DeviceConstraints are the optional per-device narrowings spec §4
// describes on top of the capability tier: allowed profiles/threads,
// approval kinds. Empty/nil slices mean "unconstrained on this axis" — a
// device with capability grants but no explicit constraint is not
// thereby MORE privileged than the tier allows; constraints only narrow,
// never widen, a capability grant.
type DeviceConstraints struct {
	AllowedProfiles []string `json:"allowed_profiles,omitempty"`
	AllowedThreads  []string `json:"allowed_threads,omitempty"`
	// AllowedApprovalKinds, if non-empty, is the exact set of
	// contracts.ApprovalKind this device may resolve even if it holds
	// CapApprover (e.g. "exec yes, patch no" — spec §4).
	AllowedApprovalKinds []contracts.ApprovalKind `json:"allowed_approval_kinds,omitempty"`
}

// DefaultCapabilities is a new device's enrollment default (spec §4:
// "agora default for a new device = observer + approver, upgrade
// explicit" — the deliberate anti-codex-trap default: never
// full-unscoped-control-by-default).
func DefaultCapabilities() []contracts.Capability {
	return []contracts.Capability{contracts.CapObserver, contracts.CapApprover}
}

// Device is one enrollment record — the binding "device X may control me
// with capabilities Y" (spec §3 step 4). The daemon's device registry is
// the sole authority the IK handshake and the capability gate consult; no
// third party.
type Device struct {
	ID           string
	StaticPubKey []byte
	Metadata     DeviceMetadata
	Capabilities []contracts.Capability
	Constraints  DeviceConstraints
	EnrolledAt   time.Time
	LastSeenAt   time.Time
	Revoked      bool
	RevokedAt    time.Time
}

// state enums for the handshake × enrollment matrix (spec §2: "completes
// only if that key is enrolled and not revoked").
type deviceState int

const (
	stateUnenrolled deviceState = iota
	stateEnrolled
	stateRevoked
)

// Registry is the daemon's device enrollment authority: enroll/revoke/list,
// keyed by device fingerprint (contracts.IdentityDevice, remote §2/§3).
// Safe for concurrent use.
type Registry struct {
	clock Clock

	mu      sync.RWMutex
	devices map[string]*Device
}

// NewRegistry creates an empty registry. clock defaults to time.Now if nil.
func NewRegistry(clock Clock) *Registry {
	if clock == nil {
		clock = time.Now
	}
	return &Registry{clock: clock, devices: make(map[string]*Device)}
}

// Enroll persists a new binding for id (the device's fingerprint) with the
// given static public key, metadata, and capabilities. Re-enrolling an
// existing, non-revoked id overwrites its record (re-pairing after a key
// change is a fresh enrollment, not a merge — spec §3 step 4 treats
// enrollment as the daemon's persisted source of truth). Re-enrolling a
// REVOKED id is refused: a revoked device must not be able to silently
// resurrect itself by replaying its old claim — an operator must
// explicitly Unrevoke first.
func (r *Registry) Enroll(id string, pubKey []byte, meta DeviceMetadata, caps []contracts.Capability) (Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.devices[id]; ok && existing.Revoked {
		return Device{}, ErrDeviceRevoked
	}
	now := r.clock()
	d := &Device{
		ID:           id,
		StaticPubKey: append([]byte(nil), pubKey...),
		Metadata:     meta,
		Capabilities: append([]contracts.Capability(nil), caps...),
		EnrolledAt:   now,
		LastSeenAt:   now,
	}
	r.devices[id] = d
	return *d, nil
}

// Revoke marks id revoked: "revocation kills live sessions keyed by that
// device and refuses future handshakes" (spec §3). Killing already-live
// sessions is the caller's job (this package has no session handle here);
// Revoke's contract is that every FUTURE lookup — handshake or capability
// check — fails closed for this id from this call onward.
func (r *Registry) Revoke(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrDeviceNotEnrolled
	}
	d.Revoked = true
	d.RevokedAt = r.clock()
	return nil
}

// Unrevoke clears a revocation, requiring an explicit operator action
// distinct from Enroll (see Enroll's doc comment) — resurrecting a device
// is never a side effect of it merely re-presenting its old claim.
func (r *Registry) Unrevoke(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrDeviceNotEnrolled
	}
	d.Revoked = false
	return nil
}

// SetCapabilities replaces id's granted capability set (spec §4: "upgrade
// explicit").
func (r *Registry) SetCapabilities(id string, caps []contracts.Capability) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrDeviceNotEnrolled
	}
	d.Capabilities = append([]contracts.Capability(nil), caps...)
	return nil
}

// SetConstraints replaces id's per-device constraints.
func (r *Registry) SetConstraints(id string, c DeviceConstraints) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrDeviceNotEnrolled
	}
	d.Constraints = c
	return nil
}

// Touch updates last_seen_at (spec §3: "last_seen_at tracking").
func (r *Registry) Touch(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.devices[id]; ok {
		d.LastSeenAt = r.clock()
	}
}

// Get returns a copy of id's record and whether it exists at all
// (enrolled or revoked — callers needing the tri-state should use state()).
func (r *Registry) Get(id string) (Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	if !ok {
		return Device{}, false
	}
	return *d, true
}

// List returns every enrolled record (device list, spec §3), revoked ones
// included — device list/revoke shows the full history, it doesn't hide
// revoked entries.
func (r *Registry) List() []Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, *d)
	}
	return out
}

// state reports the tri-state the handshake × enrollment matrix keys off:
// unenrolled (never seen), enrolled (live), revoked.
func (r *Registry) state(id string) deviceState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	if !ok {
		return stateUnenrolled
	}
	if d.Revoked {
		return stateRevoked
	}
	return stateEnrolled
}
