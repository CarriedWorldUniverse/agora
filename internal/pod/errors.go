package pod

import "errors"

// Sentinel errors. Every one of these is a REFUSAL (fail closed) — none of
// these paths ever falls through to "allow" or partial-apply on an error a
// caller ignores. Provision-path errors in particular must never leave
// mutated state behind (agora-spec-remote.md §6a: "apply-all-or-reject").
var (
	// ErrAlreadyProvisioned is returned by Provision when the pod is not
	// currently blank — provision a running pod again only after
	// Deprovision (or a fresh pod).
	ErrAlreadyProvisioned = errors.New("pod: already provisioned")

	// ErrNotProvisioned is returned by RunTurn/Deprovision when the pod is
	// still blank. Spec §6a: "boots blank ... refuses turns until
	// provisioned."
	ErrNotProvisioned = errors.New("pod: not provisioned")

	// ErrInvalidProvision covers every provision-message shape failure this
	// package validates directly (missing profile, malformed session
	// new/resume, missing identity source, malformed workspace) — distinct
	// from the remote-package scope failures (ErrProfileNotAllowed /
	// ErrThreadNotAllowed / ErrCapabilityDenied), which are returned
	// unwrapped from remote so callers can errors.Is against remote's own
	// sentinels.
	ErrInvalidProvision = errors.New("pod: invalid provision message")

	// ErrIdentityResolve wraps a failure from the injected
	// contracts.IdentityProvider — the pod cannot become "<aspect> wearing
	// <profile>" without a resolvable identity, so this always rejects the
	// whole provision (§6a atomicity).
	ErrIdentityResolve = errors.New("pod: identity resolution failed")

	// ErrResumeThreadUnknown is returned when session.resume names a thread
	// the pod's ThreadStore has never heard of — resuming a nonexistent
	// thread is a provision-message error, not a runtime one, so it is
	// caught during validation (before any mutation), not left to surface
	// later as a broken turn.
	ErrResumeThreadUnknown = errors.New("pod: resume thread unknown to the store")
)
