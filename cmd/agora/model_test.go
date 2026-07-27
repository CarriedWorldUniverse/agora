package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".agora"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agora", name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A flag must beat the config file — the operator was explicit for this run.
func TestResolveModel_FlagBeatsConfig(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "config.json", `{"default_model":"from-config"}`)
	if got := resolveModel("from-flag", cwd); got != "from-flag" {
		t.Fatalf("resolveModel = %q; want the flag value", got)
	}
}

func TestResolveModel_ConfigAppliesWithNoFlag(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "config.json", `{"default_model":"from-config"}`)
	if got := resolveModel("", cwd); got != "from-config" {
		t.Fatalf("resolveModel = %q; want the config value", got)
	}
}

func TestResolveModel_NeitherIsEmpty(t *testing.T) {
	if got := resolveModel("", t.TempDir()); got != "" {
		t.Fatalf("resolveModel = %q; want \"\" (engine default)", got)
	}
}

// A registry key resolves to its real model id AND its provider spec —
// this is what routes a turn to a non-default backend.
func TestResolveModelSpec_RegistryKeyYieldsIDAndProvider(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "models.json",
		`{"kimi":{"model":"kimi-k3","base_url":"http://gw:4000/v1"}}`)

	id, spec := resolveModelSpec("kimi", cwd)
	if id != "kimi-k3" {
		t.Errorf("id = %q; want the registry entry's real model id", id)
	}
	if spec == nil {
		t.Fatal("provider spec is nil; a base_url entry must route to a provider")
	}
	if spec.Name != "openai" || spec.BaseURL != "http://gw:4000/v1" {
		t.Errorf("spec = %+v; want the openai provider at the entry's base_url", spec)
	}
}

// A registry entry WITHOUT a base_url is a subscription-lane model: real
// id, but no provider override.
func TestResolveModelSpec_RegistryKeyWithoutBaseURLHasNoProvider(t *testing.T) {
	cwd := t.TempDir()
	writeJSON(t, cwd, "models.json", `{"opus":{"model":"claude-opus-5"}}`)

	id, spec := resolveModelSpec("opus", cwd)
	if id != "claude-opus-5" {
		t.Errorf("id = %q; want claude-opus-5", id)
	}
	if spec != nil {
		t.Errorf("spec = %+v; a no-base_url entry must stay on the default provider", spec)
	}
}

// An unknown name is a raw upstream model id, not an error — this is what
// makes `-model claude-sonnet-5` work with no registry entry at all.
func TestResolveModelSpec_UnknownNameIsARawModelID(t *testing.T) {
	id, spec := resolveModelSpec("some-raw-model", t.TempDir())
	if id != "some-raw-model" {
		t.Errorf("id = %q; want the name passed through", id)
	}
	if spec != nil {
		t.Errorf("spec = %+v; a raw id must stay on the default provider", spec)
	}
}

func TestResolveModelSpec_EmptyNameIsNoOp(t *testing.T) {
	id, spec := resolveModelSpec("", t.TempDir())
	if id != "" || spec != nil {
		t.Fatalf("resolveModelSpec(\"\") = (%q, %+v); want empty/nil", id, spec)
	}
}
