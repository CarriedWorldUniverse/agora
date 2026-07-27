package main

import (
	"os"
	"testing"
)

// setHome must actually isolate os.UserHomeDir on the platform the test
// runs on. Asserting the OUTCOME rather than the mechanism is what makes
// this portable: the previous helper set only HOME, which is a no-op on
// Windows, and every home-dependent test silently read the real profile.
func TestSetHome_IsolatesUserHomeDirOnThisPlatform(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)
	got, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if got != dir {
		t.Fatalf("os.UserHomeDir() = %q; want the isolated %q — home isolation is a no-op on this platform, so these tests read the real profile", got, dir)
	}
}

// The models.json `"default": true` flag supplies the session model when
// neither a flag nor config.json names one. This is the case `agora pipe`
// never had: before the flag it had no default at all.
func TestResolveModel_RegistryDefaultAppliesWithNoFlagOrConfig(t *testing.T) {
	isolatedHome(t)
	cwd := t.TempDir()
	writeJSON(t, cwd, "models.json", `{
		"sonnet": {"model": "claude-sonnet-5"},
		"kimi":   {"model": "kimi-k3", "default": true}
	}`)
	if got := resolveModel("", cwd); got != "kimi" {
		t.Fatalf("resolveModel = %q; want the flagged registry default kimi", got)
	}
}

// config.json is the more specific file, so it outranks the shared
// catalog's flag. Getting this backwards would make a checked-in
// models.json silently override a machine's stated preference.
func TestResolveModel_ConfigBeatsRegistryDefault(t *testing.T) {
	isolatedHome(t)
	cwd := t.TempDir()
	writeJSON(t, cwd, "config.json", `{"default_model":"from-config"}`)
	writeJSON(t, cwd, "models.json", `{"kimi": {"model": "kimi-k3", "default": true}}`)
	if got := resolveModel("", cwd); got != "from-config" {
		t.Fatalf("resolveModel = %q; want config.json to outrank the registry flag", got)
	}
}

// An explicit flag still beats everything.
func TestResolveModel_FlagBeatsRegistryDefault(t *testing.T) {
	isolatedHome(t)
	cwd := t.TempDir()
	writeJSON(t, cwd, "models.json", `{"kimi": {"model": "kimi-k3", "default": true}}`)
	if got := resolveModel("from-flag", cwd); got != "from-flag" {
		t.Fatalf("resolveModel = %q; want the explicit flag", got)
	}
}

// The flagged NAME must survive lowering to a real id + provider spec —
// resolving to the name alone would send "kimi" upstream as a model id.
func TestResolveModel_FlaggedDefaultLowersToIDAndProvider(t *testing.T) {
	isolatedHome(t)
	cwd := t.TempDir()
	writeJSON(t, cwd, "models.json", `{
		"kimi": {"model": "kimi-k3", "base_url": "http://gw.local/v1", "api_key": "dummy", "default": true}
	}`)
	id, spec := resolveModelSpec(resolveModel("", cwd), cwd)
	if id != "kimi-k3" {
		t.Fatalf("model id = %q; want the entry's real upstream id", id)
	}
	if spec == nil || spec.BaseURL != "http://gw.local/v1" {
		t.Fatalf("provider spec = %+v; want the entry's gateway route", spec)
	}
}

// A user-global default applies to a project that has no models.json of
// its own — the common case for a single-operator machine.
func TestResolveModel_GlobalDefaultAppliesToBareProject(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	writeJSON(t, home, "models.json", `{"opus": {"model": "claude-opus-4-8", "default": true}}`)
	if got := resolveModel("", t.TempDir()); got != "opus" {
		t.Fatalf("resolveModel = %q; want the user-global default opus", got)
	}
}
