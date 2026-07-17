package mcp

import (
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestParseServerConfig_Stdio(t *testing.T) {
	raw := map[string]any{
		"command": "npx",
		"args":    []any{"-y", "@some/server"},
		"env":     map[string]any{"FOO": "bar"},
		"cwd":     "/work",
	}
	c, err := ParseServerConfig("things", raw)
	if err != nil {
		t.Fatalf("ParseServerConfig: %v", err)
	}
	if c.Transport != TransportStdio {
		t.Fatalf("transport = %v, want stdio", c.Transport)
	}
	if c.Command != "npx" || len(c.Args) != 2 {
		t.Fatalf("command/args mismatch: %+v", c)
	}
	if !c.Enabled || c.Required {
		t.Fatalf("defaults wrong: enabled=%v required=%v", c.Enabled, c.Required)
	}
	if c.StartupTimeout != 30*time.Second {
		t.Fatalf("startup timeout default = %v", c.StartupTimeout)
	}
	if c.ToolTimeout != 300*time.Second {
		t.Fatalf("tool timeout default = %v", c.ToolTimeout)
	}
}

func TestParseServerConfig_HTTP(t *testing.T) {
	raw := map[string]any{
		"url":                  "https://example.com/mcp",
		"bearer_token_env_var": "MY_TOKEN",
	}
	c, err := ParseServerConfig("herald", raw)
	if err != nil {
		t.Fatalf("ParseServerConfig: %v", err)
	}
	if c.Transport != TransportHTTP || c.URL != "https://example.com/mcp" {
		t.Fatalf("http config wrong: %+v", c)
	}
}

func TestParseServerConfig_MixedTransportIsError(t *testing.T) {
	raw := map[string]any{"command": "npx", "url": "https://x"}
	_, err := ParseServerConfig("bad", raw)
	if !errors.Is(err, ErrMixedTransport) {
		t.Fatalf("err = %v, want ErrMixedTransport", err)
	}
}

func TestParseServerConfig_NoTransportOnEnabledIsError(t *testing.T) {
	_, err := ParseServerConfig("bad", map[string]any{})
	if !errors.Is(err, ErrNoTransport) {
		t.Fatalf("err = %v, want ErrNoTransport", err)
	}
}

func TestParseServerConfig_NoTransportOnDisabledIsOK(t *testing.T) {
	c, err := ParseServerConfig("bad", map[string]any{"enabled": false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Enabled {
		t.Fatalf("expected disabled")
	}
}

func TestParseServerConfig_RawBearerTokenRejected(t *testing.T) {
	raw := map[string]any{"url": "https://x", "bearer_token": "literal-secret"}
	_, err := ParseServerConfig("bad", raw)
	if !errors.Is(err, ErrRawBearerToken) {
		t.Fatalf("err = %v, want ErrRawBearerToken", err)
	}
}

func TestParseServerConfig_WasmRecognizedButUnsupported(t *testing.T) {
	raw := map[string]any{"module": "embedded:comms", "module_hash": "sha256:abc"}
	_, err := ParseServerConfig("comms", raw)
	if !errors.Is(err, ErrWasmUnsupported) {
		t.Fatalf("err = %v, want ErrWasmUnsupported", err)
	}
}

func TestParseServerConfig_StartupTimeoutSecWinsOverMs(t *testing.T) {
	raw := map[string]any{
		"command":             "npx",
		"startup_timeout_ms":  float64(1000),
		"startup_timeout_sec": float64(9),
	}
	c, err := ParseServerConfig("x", raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.StartupTimeout != 9*time.Second {
		t.Fatalf("timeout = %v, want 9s (sec wins per §1)", c.StartupTimeout)
	}
}

func TestParseServerConfig_MissingCommand(t *testing.T) {
	_, err := ParseServerConfig("x", map[string]any{"command": ""})
	if !errors.Is(err, ErrMissingCommand) {
		t.Fatalf("err = %v, want ErrMissingCommand", err)
	}
}

func TestServerConfig_ToolAllowed(t *testing.T) {
	tests := []struct {
		name    string
		enabled []string
		disable []string
		tool    string
		want    bool
	}{
		{"no filters", nil, nil, "anything", true},
		{"allow-list contains", []string{"a", "b"}, nil, "a", true},
		{"allow-list excludes", []string{"a", "b"}, nil, "c", false},
		{"deny-list applied after allow", []string{"a"}, []string{"a"}, "a", false},
		{"deny-list only", nil, []string{"a"}, "a", false},
		{"deny-list only, other tool ok", nil, []string{"a"}, "b", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ServerConfig{EnabledTools: tt.enabled, DisabledTools: tt.disable}
			if got := c.ToolAllowed(tt.tool); got != tt.want {
				t.Errorf("ToolAllowed(%q) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}

func TestInterpolate(t *testing.T) {
	id := contracts.Identity{ID: "shadow", Fingerprint: "agora:abc123"}
	got := Interpolate("hello {identity} / {identity.fingerprint}", id)
	want := "hello shadow / agora:abc123"
	if got != want {
		t.Errorf("Interpolate = %q, want %q", got, want)
	}
}

func TestInterpolateConfig_DistinctIdentitiesFingerprintDifferently(t *testing.T) {
	c := ServerConfig{
		Name: "comms",
		Args: []string{"--id", "{identity}"},
		Env:  map[string]string{"IDENTITY": "{identity}"},
	}
	a := InterpolateConfig(c, contracts.Identity{ID: "shadow"})
	b := InterpolateConfig(c, contracts.Identity{ID: "anvil"})
	if a.Args[1] == b.Args[1] {
		t.Fatalf("expected distinct interpolated args for distinct identities, got %q for both", a.Args[1])
	}
	if a.Env["IDENTITY"] != "shadow" || b.Env["IDENTITY"] != "anvil" {
		t.Fatalf("interpolated env wrong: a=%+v b=%+v", a.Env, b.Env)
	}
}

func TestLoadServersJSON_WrappedAndBare(t *testing.T) {
	wrapped := []byte(`{"mcpServers":{"foo":{"command":"npx","args":["-y","foo"]}}}`)
	servers, err := LoadServersJSON(wrapped)
	if err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	if servers["foo"].Command != "npx" {
		t.Fatalf("wrapped parse wrong: %+v", servers)
	}

	bare := []byte(`{"foo":{"command":"npx"}}`)
	servers2, err := LoadServersJSON(bare)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if servers2["foo"].Command != "npx" {
		t.Fatalf("bare parse wrong: %+v", servers2)
	}
}

func TestParseServers_DeterministicOrder(t *testing.T) {
	table := map[string]map[string]any{
		"zeta":  {"command": "z"},
		"alpha": {"command": "a"},
	}
	servers, err := ParseServers(table)
	if err != nil {
		t.Fatal(err)
	}
	names := SortedNames(servers)
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Fatalf("SortedNames = %v", names)
	}
}
