package turnengine

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// Every builtin preset must report its own name — the whole point of the
// fix is that a hook can tell which posture the session is running.
func TestPermissionModeName_ReportsEachBuiltinPreset(t *testing.T) {
	for name, set := range contracts.BuiltinPresets() {
		if got := permissionModeName(set); got != name {
			t.Errorf("permissionModeName(%s preset) = %q; want %q", name, got, name)
		}
	}
}

// The engine's zero-config policy is not one of the four presets; it gets
// its own name rather than being mislabelled as one of them.
func TestPermissionModeName_ZeroConfigPolicyIsSandboxAuto(t *testing.T) {
	if got := permissionModeName(defaultPolicy()); got != "sandbox-auto" {
		t.Fatalf("permissionModeName(defaultPolicy()) = %q; want sandbox-auto", got)
	}
}

// An operator-defined PolicySet must not be reported as the nearest
// preset — that would tell a hook something false.
func TestPermissionModeName_CustomPolicyIsCustom(t *testing.T) {
	custom := contracts.PolicySet{
		contracts.KindExec:       contracts.PolicyDeny,
		contracts.KindPatch:      contracts.PolicyPrompt,
		contracts.KindEscalation: contracts.PolicyDeny,
	}
	if got := permissionModeName(custom); got != PermissionModeCustom {
		t.Fatalf("permissionModeName(custom) = %q; want %q", got, PermissionModeCustom)
	}
}

// The old bug in one assertion: the reported mode must TRACK the policy,
// not be a constant. A prompt-preset Manager and a never-escalate Manager
// must not report the same thing.
func TestPermissionModeName_DistinguishesPolicies(t *testing.T) {
	presets := contracts.BuiltinPresets()
	prompt := permissionModeName(presets[contracts.PresetPrompt])
	never := permissionModeName(presets[contracts.PresetNeverEscalate])
	if prompt == never {
		t.Fatalf("prompt and never-escalate both report %q — permission_mode is not tracking policy", prompt)
	}
	if prompt != contracts.PresetPrompt || never != contracts.PresetNeverEscalate {
		t.Fatalf("got (%q, %q); want (%q, %q)", prompt, never, contracts.PresetPrompt, contracts.PresetNeverEscalate)
	}
}

// A PolicySet that merely has the same LENGTH as a preset but different
// values must not match it.
func TestPolicySetsEqual_LengthAloneIsNotEnough(t *testing.T) {
	a := contracts.PolicySet{contracts.KindExec: contracts.PolicyAuto}
	b := contracts.PolicySet{contracts.KindExec: contracts.PolicyDeny}
	if policySetsEqual(a, b) {
		t.Fatal("policy sets with the same key but different values compared equal")
	}
	c := contracts.PolicySet{contracts.KindPatch: contracts.PolicyAuto}
	if policySetsEqual(a, c) {
		t.Fatal("policy sets with different keys compared equal")
	}
}

// A nil HookRunner must not panic — nil is the documented "no hooks
// configured" value that DiscoverHooks returns for most operators.
func TestHookRunner_ReportedPermissionMode_NilSafe(t *testing.T) {
	var hr *HookRunner
	if got := hr.reportedPermissionMode(); got == "" {
		t.Fatal("nil HookRunner reported an empty permission mode")
	}
	hr.setPermissionMode("prompt") // must not panic
}

// An unclaimed runner (built by DiscoverHooks, no Manager yet) reports the
// engine default rather than an empty string.
func TestHookRunner_ReportedPermissionMode_UnclaimedRunner(t *testing.T) {
	hr := &HookRunner{}
	if got := hr.reportedPermissionMode(); got != "sandbox-auto" {
		t.Fatalf("unclaimed runner reported %q; want the engine default posture", got)
	}
	hr.setPermissionMode(contracts.PresetStrict)
	if got := hr.reportedPermissionMode(); got != contracts.PresetStrict {
		t.Fatalf("after setPermissionMode, reported %q; want %q", got, contracts.PresetStrict)
	}
}

// End-to-end through the Manager: building one with a given policy must
// leave its hook runner reporting that policy.
func TestManager_TellsHookRunnerItsActualPolicy(t *testing.T) {
	hr := &HookRunner{}
	presets := contracts.BuiltinPresets()
	_ = NewManager("th_pm", fake.NewProvider(fake.Step{Text: "hi"}),
		WithHooks(hr), WithPolicy(presets[contracts.PresetStrict]))
	if got := hr.reportedPermissionMode(); got != contracts.PresetStrict {
		t.Fatalf("hook runner reports %q; want %q — the Manager did not hand over its policy",
			got, contracts.PresetStrict)
	}
}
