package mcp

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestInterpolateIdentity(t *testing.T) {
	in := map[string]ServerConfig{
		"comms": {
			Name:        "comms",
			Command:     "/bin/{identity}-comms",
			Args:        []string{"-id", "{identity}", "-fp", "{identity.fingerprint}"},
			Env:         map[string]string{"WHO": "{identity.id}", "KIND": "{identity.kind}"},
			EnvVars:     []EnvVarRef{{Name: "HOME"}}, // NAME — must NOT be interpolated
			HTTPHeaders: map[string]string{"X-Id": "{identity.display_name}"},
			URL:         "https://x/{identity}/mcp",
		},
	}
	id := contracts.Identity{ID: "anvil", Fingerprint: "agora:abc123", Kind: contracts.IdentityAspect, DisplayName: "Anvil"}
	out := InterpolateIdentity(in, id)
	c := out["comms"]

	if c.Command != "/bin/anvil-comms" {
		t.Errorf("Command = %q", c.Command)
	}
	if c.Args[1] != "anvil" || c.Args[3] != "agora:abc123" {
		t.Errorf("Args = %v", c.Args)
	}
	if c.Env["WHO"] != "anvil" || c.Env["KIND"] != "aspect" {
		t.Errorf("Env = %v", c.Env)
	}
	if c.HTTPHeaders["X-Id"] != "Anvil" {
		t.Errorf("HTTPHeaders = %v", c.HTTPHeaders)
	}
	if c.URL != "https://x/anvil/mcp" {
		t.Errorf("URL = %q", c.URL)
	}
	if c.EnvVars[0].Name != "HOME" {
		t.Errorf("EnvVars name was interpolated: %v", c.EnvVars)
	}
	// Unknown placeholder left verbatim.
	in2 := map[string]ServerConfig{"x": {Command: "{identity.future_field}"}}
	if got := InterpolateIdentity(in2, id)["x"].Command; got != "{identity.future_field}" {
		t.Errorf("unknown placeholder = %q, want left verbatim", got)
	}
}
