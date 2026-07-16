package contracts

// IdentityKind classifies who an identity belongs to.
// Spec: agora-spec.md §Identity ("kind = operator | aspect | service | device").
type IdentityKind string

const (
	IdentityOperator IdentityKind = "operator"
	IdentityAspect   IdentityKind = "aspect"
	IdentityService  IdentityKind = "service"
	// IdentityDevice is a controller key enrolled for remote control.
	// Spec: agora-spec-remote.md §2 (device identities).
	IdentityDevice IdentityKind = "device"
)

// Identity is the resolved acting identity of an instance, immutable for the
// session. The keypair IS the identity: Fingerprint is authoritative; ID is a
// petname/label, never a trust anchor.
// Spec: agora-spec.md §Identity, §Identity bytes.
type Identity struct {
	// ID is the petname (e.g. "shadow"). Display/config only.
	ID string `json:"id"`
	// Fingerprint is the authoritative id: "agora:<base32(sha256(pubkey))[..16]>".
	Fingerprint string       `json:"fingerprint"`
	Kind        IdentityKind `json:"kind"`
	DisplayName string       `json:"display_name,omitempty"`
	// Source records where the bytes came from: "local", "keyring:<ref>",
	// "herald:<name>". Spec: agora-spec.md §Identity sources.
	Source string `json:"source,omitempty"`
	// Ephemeral marks herald provision-mode short-TTL identities (dispatch
	// pods). Never the default. Spec: agora-spec.md §Identity sources.
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// IdentityProvider resolves identity bytes from a source. Semantics align
// with W3C DIDs deliberately; v1 ships local/keyring/herald built-in only.
// Spec: agora-spec.md §Identity sources ("pluggable via standard").
type IdentityProvider interface {
	// Resolve returns the identity (and loads/generates key material as the
	// source dictates). Resolution precedence across sources is the caller's
	// concern: dispatch envelope > flag > profile > daemon default > OS user.
	Resolve(ref string) (Identity, error)
}
