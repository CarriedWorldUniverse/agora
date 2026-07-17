package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// TrustState is the trust status of a handler at resolution time.
// Spec: agora-spec-hooks.md §4.4.
type TrustState string

const (
	// TrustTrusted: recorded trusted_hash matches the computed content hash
	// (or the handler is managed/bypass_trust).
	TrustTrusted TrustState = "Trusted"
	// TrustModified: a trusted_hash IS recorded but does not match — the
	// handler command/matcher/event changed since it was trusted.
	TrustModified TrustState = "Modified"
	// TrustUntrusted: no trusted_hash is recorded at all.
	TrustUntrusted TrustState = "Untrusted"
)

// normalizeCommand normalizes a handler command string for content
// hashing. The spec (§4.4) says only "normalized command", without
// specifying the normalization — the simplest spec-consistent reading that
// still makes the stated goal ("equal hooks from TOML and JSON converge")
// true is whitespace normalization: trim, and collapse internal runs of
// whitespace to a single space. This is a deliberate, narrow reading, not
// an invented feature — TOML and JSON can differ only in incidental
// whitespace around an otherwise-identical shell command.
func normalizeCommand(command string) string {
	fields := strings.Fields(command)
	return strings.Join(fields, " ")
}

// ContentHash computes the trust hash over the normalized identity: event +
// matcher + normalized command (§4.4). Equal hooks from TOML and JSON
// converge because both feed the same normalized triple through the same
// function. The matcher is taken as written (not normalized) — a matcher
// edit is a real semantic change to what the hook fires on, and must
// invalidate trust.
func ContentHash(event EventName, matcher, command, commandWindows string) string {
	// Fold in BOTH OS command variants: EffectiveCommand runs CommandWindows
	// on Windows, so a hash over only Command let an attacker ship a benign
	// reviewed Command (matching the trusted hash) plus a malicious
	// CommandWindows that actually executes — a trust-gate bypass (review U9).
	sum := sha256.Sum256([]byte(string(event) + "\x00" + matcher + "\x00" + normalizeCommand(command) + "\x00" + normalizeCommand(commandWindows)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HandlerState is the per-handler enable/trust record, keyed by
// RegisteredHandler.PositionalKey(). Spec §4.4: "State (enabled,
// trusted_hash) read only from User(+session) layers; later layers merge
// field-by-field." This package models the ALREADY-MERGED result — merging
// layered state files is out of this unit's scope (config-loading concern);
// Registry.Resolve just takes the final map.
type HandlerState struct {
	// Enabled defaults to true when no explicit state record exists for a
	// handler (ambiguity call: the spec doesn't say what "absent" means for
	// enabled, only for trusted_hash where absent is explicitly Untrusted;
	// the simplest reading that doesn't silently disable every
	// newly-discovered hook is "unknown enabled = enabled", gated by the
	// separate, spec-explicit trust check below).
	Enabled bool
	// TrustedHash is the recorded "sha256:..." hash, or "" if none is
	// recorded (Untrusted per §4.4).
	TrustedHash string
}

// ResolveTrust decides whether a handler may run, per §4.4: "Run only if
// enabled && (bypass_trust || managed || hash matches trusted_hash);
// mismatch → status Modified; absent → Untrusted." managed and bypassTrust
// short-circuit straight to Trusted+runnable regardless of the recorded
// hash (§4.3: "Managed hooks are always enabled and trusted"). stateKnown
// distinguishes "no state record at all" (Enabled defaults true) from an
// explicit record with Enabled=false.
//
// Untrusted/Modified are reported (not silently dropped) so the caller can
// still LIST the handler for the UI ("trust this hook") per §4.4's closing
// sentence — but runnable is always false for them: this is the fail-closed
// gate (ground rule 6: an untrusted/modified hook MUST NOT execute).
func ResolveTrust(state HandlerState, stateKnown bool, computedHash string, managed, bypassTrust bool) (runnable bool, enabled bool, status TrustState) {
	enabled = true
	if stateKnown {
		enabled = state.Enabled
	}
	if !enabled {
		return false, false, trustStatusFor(state.TrustedHash, computedHash)
	}
	if managed || bypassTrust {
		return true, true, TrustTrusted
	}
	status = trustStatusFor(state.TrustedHash, computedHash)
	return status == TrustTrusted, true, status
}

func trustStatusFor(trustedHash, computedHash string) TrustState {
	if trustedHash == "" {
		return TrustUntrusted
	}
	if trustedHash != computedHash {
		return TrustModified
	}
	return TrustTrusted
}
