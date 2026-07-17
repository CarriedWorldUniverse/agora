// Package remote — remote control: MLE handshake, pairing, capabilities,
// device management, approval queue/timeout, and reconnect/replay.
//
// Build unit: U16 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-remote.md §1–5, §8–9 (interchange relay §7,
// browser-wasm §3a, and push notifiers §8 are v1.1 — OUT of scope here).
//
// This package is the auth layer internal/io (U2) fronts: io's Session
// trusts whatever AttachInfo/Capabilities it is handed (io/session.go's
// "DEFERRED (not this unit)" notes); remote is the unit that resolves those
// deferrals — a client's declared identity is worthless until this package
// has authenticated the device (Handshake) and its capabilities come from
// the enrollment record (Registry), never from client-declared input.
//
// Layout:
//
//   - keys.go: device/daemon static identity — X25519 keypairs (crypto/ecdh)
//     and the fingerprint scheme shared with contracts.Identity
//     (agora-spec.md §Identity, agora-spec-remote.md §2).
//   - handshake.go: the classical IK handshake (§2) — "fall back to
//     classical Noise_IK_25519_AESGCM_SHA256 for v1" — gated on Registry
//     lookup so only an enrolled, non-revoked device completes it.
//   - registry.go: device enrollment records + state (enrolled/revoked),
//     the authority the handshake and the capability gate both consult.
//   - pairing.go: the passkey-style pairing ceremony (§3) that produces
//     Registry entries.
//   - capability.go: the capability gate over contracts.Holds/
//     RequiredForInput/RequiredForApproval (§4) — derives a session's
//     capabilities from the AUTHENTICATED device's grant, never the client.
//     Also exports CheckProfile/CheckThread over AllowedProfiles/
//     AllowedThreads (§4); U18's profile-switch and thread-attach paths
//     MUST call these — this package has no in-unit call site for either
//     decision (see capability.go's HANDOFF NOTE comments).
//   - queue.go: the approval queue + timeout fallback (§8) — deny-on-timeout
//     for permission-shaped kinds, PARK (never deny-fabricate) for
//     contracts.KindQuestion.
//   - replay.go: reconnect gap-replay (§9) — per-device/thread last-seq
//     tracking so a reattaching device gets exactly what it missed.
//
// Scope discipline: the actual bytes-on-the-wire transport is internal/io
// (already built); this package models the AUTH/PAIRING/CAPABILITY/QUEUE
// state machine over an injectable seam (io.AttachInfo/io.Session for the
// capability gate; plain function args elsewhere) so it has no hard
// dependency on a concrete net.Conn/websocket. Daemon wiring is U18
// (internal/daemon).
package remote
