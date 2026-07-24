package turnengine

import (
	"sort"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// permissionmode.go derives the permission_mode string hooks receive from
// the policy actually in force.
//
// This field used to be the literal "default", always, with a doc comment
// explaining that real derivation "depends on a profile/preset resolution
// this engine doesn't have yet". It does have it now: the Manager holds a
// contracts.PolicySet, and the builtin presets are named. Reporting the
// preset the session is actually running removes a field that was
// misinforming every hook that read it.
//
// It stays REPORT-ONLY, exactly as agora-spec-approvals.md §3 requires —
// hooks never *configure* via this field, and nothing in the engine's own
// decision-making reads it back. This changes what hooks are TOLD, not what
// the engine does.

// PermissionModeCustom is reported when the policy in force is not one of
// the builtin presets — an operator-defined PolicySet. Naming it rather
// than picking the closest preset keeps the report honest: a hook that
// wants to key off an exact policy should not be told "prompt" when the
// operator has hand-tuned something else.
const PermissionModeCustom = "custom"

// permissionModeName returns the builtin preset name whose PolicySet equals
// p, or PermissionModeCustom when none does. A nil/empty policy reports the
// engine's own zero-config posture (defaultPolicy) if that matches, and
// otherwise custom — it never silently reports "default" for something that
// isn't.
func permissionModeName(p contracts.PolicySet) string {
	if name, ok := matchPreset(p, contracts.BuiltinPresets()); ok {
		return name
	}
	// The Manager's zero-config sandbox-first policy is not one of the four
	// builtin presets, so it gets its own name rather than "custom" — it is
	// a specific, documented posture (see defaultPolicy in approval.go) and
	// hooks should be able to recognise it.
	if policySetsEqual(p, defaultPolicy()) {
		return "sandbox-auto"
	}
	return PermissionModeCustom
}

// matchPreset finds the preset equal to p. Iteration is over sorted names
// so the answer is deterministic if two presets ever coincide.
func matchPreset(p contracts.PolicySet, presets map[string]contracts.PolicySet) (string, bool) {
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if policySetsEqual(p, presets[name]) {
			return name, true
		}
	}
	return "", false
}

// policySetsEqual compares two PolicySets by content. Kinds absent from
// either map are treated as absent in both — a PolicySet is a sparse map
// (KindGate is deliberately absent from every preset, for instance), so
// length-then-lookup is the correct comparison.
func policySetsEqual(a, b contracts.PolicySet) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va != vb {
			return false
		}
	}
	return true
}
