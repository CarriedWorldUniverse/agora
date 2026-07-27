package tui

import "testing"

// A single flagged entry is the registry's default.
func TestRegistryDefault_FlaggedEntryWins(t *testing.T) {
	home := t.TempDir()
	writeModels(t, home, `{
		"sonnet": {"model": "claude-sonnet-5"},
		"kimi":   {"model": "kimi-k3", "default": true}
	}`)
	if got := LoadModelRegistry(home, t.TempDir()).Default(); got != "kimi" {
		t.Fatalf("Default() = %q; want the flagged entry kimi", got)
	}
}

// No flag anywhere means no default — the caller then falls through to the
// engine's own, which is what every pre-flag config relies on.
func TestRegistryDefault_NoFlagIsEmpty(t *testing.T) {
	home := t.TempDir()
	writeModels(t, home, `{"sonnet": {"model": "claude-sonnet-5"}}`)
	if got := LoadModelRegistry(home, t.TempDir()).Default(); got != "" {
		t.Fatalf("Default() = %q; want \"\" when nothing is flagged", got)
	}
}

// A project file's default supersedes the user-global one. Without the
// supersede rule BOTH would stay flagged after the merge and the winner
// would depend on map order — a session model that changes run to run.
func TestRegistryDefault_ProjectSupersedesGlobal(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeModels(t, home, `{"opus": {"model": "claude-opus-4-8", "default": true}}`)
	writeModels(t, cwd, `{"kimi": {"model": "kimi-k3", "default": true}}`)

	reg := LoadModelRegistry(home, cwd)
	if got := reg.Default(); got != "kimi" {
		t.Fatalf("Default() = %q; want the project's kimi", got)
	}
	if reg["opus"].Default {
		t.Error("the global default stayed flagged after being superseded — two defaults can now exist at once")
	}
}

// A project file that defines models but flags NO default leaves the
// global default standing: silence is not an override.
func TestRegistryDefault_SilentProjectKeepsGlobalDefault(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeModels(t, home, `{"opus": {"model": "claude-opus-4-8", "default": true}}`)
	writeModels(t, cwd, `{"haiku": {"model": "claude-haiku-4-5"}}`)
	if got := LoadModelRegistry(home, cwd).Default(); got != "opus" {
		t.Fatalf("Default() = %q; want the global opus to survive a silent project file", got)
	}
}

// Several flags in ONE file must resolve deterministically rather than by
// map order, and must leave exactly one entry flagged.
func TestRegistryDefault_MultipleFlagsAreDeterministic(t *testing.T) {
	home := t.TempDir()
	writeModels(t, home, `{
		"zeta":  {"model": "z", "default": true},
		"alpha": {"model": "a", "default": true},
		"mid":   {"model": "m", "default": true}
	}`)
	// Run repeatedly: Go randomises map iteration, so an order-dependent
	// implementation passes once and fails later.
	for i := 0; i < 50; i++ {
		reg := LoadModelRegistry(home, t.TempDir())
		if got := reg.Default(); got != "alpha" {
			t.Fatalf("iteration %d: Default() = %q; want the deterministic alphabetical winner alpha", i, got)
		}
		var flagged int
		for _, e := range reg {
			if e.Default {
				flagged++
			}
		}
		if flagged != 1 {
			t.Fatalf("iteration %d: %d entries left flagged; want exactly 1", i, flagged)
		}
	}
}

// Redefining the flagged entry without the flag drops the default: the
// project's definition of that name wins wholesale, flag included.
func TestRegistryDefault_RedefiningFlaggedEntryDropsIt(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeModels(t, home, `{"opus": {"model": "claude-opus-4-8", "default": true}}`)
	writeModels(t, cwd, `{"opus": {"model": "claude-opus-5"}}`)

	reg := LoadModelRegistry(home, cwd)
	if got := reg["opus"].Model; got != "claude-opus-5" {
		t.Fatalf("opus.Model = %q; want the project's override", got)
	}
	if got := reg.Default(); got != "" {
		t.Fatalf("Default() = %q; want \"\" — the project redefined opus without the flag", got)
	}
}
