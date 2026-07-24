package turnengine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".agora"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agora", "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// PolicyForMode and permissionModeName must be exact inverses — otherwise
// the name an operator selects and the name hooks are told could differ.
func TestPolicyForMode_IsTheInverseOfPermissionModeName(t *testing.T) {
	for _, name := range KnownModes() {
		p, ok := PolicyForMode(name)
		if !ok {
			t.Fatalf("KnownModes lists %q but PolicyForMode rejects it", name)
		}
		if got := permissionModeName(p); got != name {
			t.Errorf("round trip broke: PolicyForMode(%q) reports back as %q", name, got)
		}
	}
}

func TestPolicyForMode_UnknownIsRejected(t *testing.T) {
	for _, name := range []string{"", "bogus", "Prompt", "auto safe", "yolo"} {
		if _, ok := PolicyForMode(name); ok {
			t.Errorf("PolicyForMode(%q) accepted an unknown mode", name)
		}
	}
}

func TestKnownModes_CoversEveryBuiltinPresetPlusSandboxAuto(t *testing.T) {
	got := map[string]bool{}
	for _, n := range KnownModes() {
		got[n] = true
	}
	for name := range contracts.BuiltinPresets() {
		if !got[name] {
			t.Errorf("KnownModes omits builtin preset %q", name)
		}
	}
	if !got[SandboxAutoMode] {
		t.Error("KnownModes omits sandbox-auto")
	}
}

func TestDescribeMode_EveryKnownModeHasADescription(t *testing.T) {
	for _, n := range KnownModes() {
		if DescribeMode(n) == "" {
			t.Errorf("mode %q has no description; it appears in -mode help", n)
		}
	}
}

func TestLoadPermissionMode_ProjectOverridesUser(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeConfig(t, home, `{"permission_mode":"strict"}`)
	writeConfig(t, cwd, `{"permission_mode":"auto-safe"}`)

	if got := LoadPermissionMode(home, cwd); got != contracts.PresetAutoSafe {
		t.Fatalf("LoadPermissionMode = %q; want the project value auto-safe", got)
	}
}

func TestLoadPermissionMode_UserAppliesWhenProjectIsSilent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeConfig(t, home, `{"permission_mode":"strict"}`)
	writeConfig(t, cwd, `{"default_effort":"low"}`) // unrelated key only

	if got := LoadPermissionMode(home, cwd); got != contracts.PresetStrict {
		t.Fatalf("LoadPermissionMode = %q; want the user value strict", got)
	}
}

// A typo must NOT quietly hand the session a different posture than the
// operator wrote — it is skipped, leaving the engine default, and warned
// about on stderr.
func TestLoadPermissionMode_UnknownValueIsSkippedNotSubstituted(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeConfig(t, cwd, `{"permission_mode":"nver-escalate"}`) // typo

	if got := LoadPermissionMode(home, cwd); got != "" {
		t.Fatalf("LoadPermissionMode = %q; a typo must resolve to \"\" (engine default), not a guess", got)
	}
}

func TestLoadPermissionMode_CorruptFileIsSkipped(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeConfig(t, cwd, `{not json`)
	if got := LoadPermissionMode(home, cwd); got != "" {
		t.Fatalf("LoadPermissionMode = %q; a corrupt file must be skipped", got)
	}
}

func TestLoadPermissionMode_NoConfigIsEmpty(t *testing.T) {
	if got := LoadPermissionMode(t.TempDir(), t.TempDir()); got != "" {
		t.Fatalf("LoadPermissionMode = %q; want \"\" with no config anywhere", got)
	}
}

// The wiring that matters: the selected mode must actually become the
// Manager's policy, and be what hooks are told.
func TestWithPermissionMode_SetsTheManagersPolicy(t *testing.T) {
	for _, name := range KnownModes() {
		hr := &HookRunner{}
		_ = NewManager("th_mode", fake.NewProvider(fake.Step{Text: "hi"}),
			WithHooks(hr), WithPermissionMode(name))
		if got := hr.reportedPermissionMode(); got != name {
			t.Errorf("WithPermissionMode(%q) produced a session reporting %q", name, got)
		}
	}
}

// An unknown name must leave the policy untouched rather than clearing it —
// a nil PolicySet would fail-closed-ask on every kind, which is a different
// posture again.
func TestWithPermissionMode_UnknownNameLeavesPolicyUntouched(t *testing.T) {
	hr := &HookRunner{}
	_ = NewManager("th_mode_bad", fake.NewProvider(fake.Step{Text: "hi"}),
		WithHooks(hr),
		WithPolicy(contracts.BuiltinPresets()[contracts.PresetStrict]),
		WithPermissionMode("bogus"))
	if got := hr.reportedPermissionMode(); got != contracts.PresetStrict {
		t.Fatalf("an unknown mode changed the policy to %q; want strict left in place", got)
	}
}

// never-escalate is the posture that makes unattended runs possible; prove
// it actually denies rather than prompting.
func TestPolicyForMode_NeverEscalateDeniesEscalation(t *testing.T) {
	p, ok := PolicyForMode(contracts.PresetNeverEscalate)
	if !ok {
		t.Fatal("never-escalate is not selectable")
	}
	if p[contracts.KindEscalation] != contracts.PolicyDeny {
		t.Fatalf("never-escalate escalation policy = %q; want deny (it must not park an unattended run)",
			p[contracts.KindEscalation])
	}
}
