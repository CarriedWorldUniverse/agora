package remote

import "errors"

// Sentinel errors. Every one of them is a REFUSAL (fail closed, ground
// rule 6) — none of these paths ever falls through to "allow" on an error
// a caller ignores.
var (
	// ErrDeviceNotEnrolled is returned when a handshake, capability check,
	// or registry mutation references a device id the registry has never
	// enrolled. Spec §2: "completes only if that key is enrolled".
	ErrDeviceNotEnrolled = errors.New("remote: device is not enrolled")

	// ErrDeviceRevoked is returned when a handshake or capability check
	// references a device the operator has revoked. Spec §2/§3:
	// "not revoked" / "revocation ... refuses future handshakes".
	ErrDeviceRevoked = errors.New("remote: device is revoked")

	// ErrHandshakeFailed covers any IK handshake cryptographic failure:
	// bad DH output, AEAD open failure, prologue mismatch, malformed
	// message. Deliberately undifferentiated on the wire (no oracle for an
	// attacker to distinguish "wrong key" from "enrolled but tampered
	// frame" from) — differentiated detail is only in the returned Go
	// error chain for local logging.
	ErrHandshakeFailed = errors.New("remote: IK handshake failed")

	// ErrPrologueMismatch is wrapped into ErrHandshakeFailed when the bound
	// (daemon_id, device_id, stream_id, channel-epoch) prologue the
	// initiator supplied doesn't match what the responder expects — spec
	// §2's cross-binding/replay protection.
	ErrPrologueMismatch = errors.New("remote: handshake prologue mismatch")

	// ErrPairingCodeUnknown, ErrPairingCodeExpired, ErrPairingCodeUsed cover
	// the pairing ceremony's short-TTL single-use code (spec §3 step 1).
	ErrPairingCodeUnknown = errors.New("remote: unknown pairing code")
	ErrPairingCodeExpired = errors.New("remote: pairing code expired")
	ErrPairingCodeUsed    = errors.New("remote: pairing code already used")

	// ErrPairingNotClaimed is returned by Approve when no device has
	// claimed the code yet — nothing for the operator to approve.
	ErrPairingNotClaimed = errors.New("remote: pairing code has not been claimed yet")

	// ErrCapabilityDenied is returned when an authenticated device attempts
	// an input/approval its granted capabilities (or constraints) don't
	// cover. Spec §4: "every inbound message is checked against them".
	ErrCapabilityDenied = errors.New("remote: device capability does not permit this")

	// ErrApprovalUnknown is returned when a queue operation references an
	// id the queue never enqueued (or has already fully resolved and
	// evicted).
	ErrApprovalUnknown = errors.New("remote: unknown approval queue entry")

	// ErrApprovalAlreadyResolved mirrors io.ErrAlreadyResolved for the
	// queue's own bookkeeping (first-answer-wins, io spec §0a) — kept as a
	// distinct sentinel here because the queue can detect this before ever
	// reaching io.Session.
	ErrApprovalAlreadyResolved = errors.New("remote: approval already resolved")

	// ErrApprovalAlreadyQueued is returned by Queue.Enqueue for an id
	// already active in the queue (resolved, unresolved, or parked).
	ErrApprovalAlreadyQueued = errors.New("remote: approval id already queued")

	// ErrQuestionNeedsAnswer is returned by Queue.Resolve for a
	// contracts.KindQuestion entry — questions resolve via
	// Queue.AnswerQuestion (a structured Answer), never a bare
	// allow/deny Decision (contracts.RequiredForApproval /
	// approvals invariant 5, remote §8).
	ErrQuestionNeedsAnswer = errors.New("remote: question kind resolves via AnswerQuestion, not Resolve")

	// ErrNotAQuestion is returned by Queue.AnswerQuestion for any entry
	// whose kind is not contracts.KindQuestion.
	ErrNotAQuestion = errors.New("remote: entry is not a question")

	// ErrGapWindowExceeded is returned by the gap-replay computation when a
	// reconnecting device's last-known seq is older than the retained
	// backlog window — the caller falls back to full-tail replay (spec §9:
	// "bounded window; beyond it → full-tail replay"), not an error the
	// device sees as a refusal.
	ErrGapWindowExceeded = errors.New("remote: gap window exceeded, full-tail replay required")
)
