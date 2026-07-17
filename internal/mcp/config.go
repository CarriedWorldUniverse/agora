package mcp

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Transport is chosen by which single transport field a server config sets.
// Spec: agora-spec-mcp.md §1.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "streamable_http"
	// TransportWasm is recognized (so a config naming it fails closed with
	// ErrWasmUnsupported) but not implemented — §1a is v1.1, out of scope.
	TransportWasm Transport = "wasm"
)

// AuthMode is the server's declared auth kind (§1 "auth" field). agora drops
// codex's "chatgpt" variant; the slot is kept for a later herald mode.
type AuthMode string

const (
	AuthOAuth AuthMode = "oauth"
)

// ApprovalMode mirrors the per-tool/per-server approval_mode strings §1
// defines. Kept as a string type (not contracts.PolicyValue) because the
// vocabulary here is the MCP-config surface's own ("auto|prompt|writes|approve"),
// distinct from the approval package's PolicyValue lattice; the MCP unit
// resolves per-server modes into an approval decision (approvals §2's
// PolicyPerServer), it does not reuse the same enum for the raw config value.
type ApprovalMode string

const (
	ApprovalAuto    ApprovalMode = "auto"
	ApprovalPrompt  ApprovalMode = "prompt"
	ApprovalWrites  ApprovalMode = "writes"
	ApprovalApprove ApprovalMode = "approve"
)

// EnvVarRef is one entry of the stdio env_vars list: a host env var name to
// forward, and where its value should come from.
// Spec: agora-spec-mcp.md §1 ("names to forward from host env; entries are
// \"NAME\" or {name, source: local|remote}").
type EnvVarRef struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"` // "local" | "remote", default "local"
}

// ServerConfig is one [mcp_servers.<name>] / mcpServers["<name>"] entry,
// fully resolved (defaults applied, {identity} interpolated). Spec: §1.
type ServerConfig struct {
	Name string

	Transport Transport

	// stdio
	Command string
	Args    []string
	Env     map[string]string
	EnvVars []EnvVarRef
	Cwd     string

	// streamable_http
	URL               string
	BearerTokenEnvVar string
	HTTPHeaders       map[string]string
	EnvHTTPHeaders    map[string]string

	// wasm (§1a, recognized but not implemented by this unit)
	Module     string
	ModuleHash string

	// Shared
	Enabled                   bool
	Required                  bool
	StartupTimeout            time.Duration
	ToolTimeout               time.Duration
	SupportsParallelToolCalls bool
	EnabledTools              []string
	DisabledTools             []string
	ToolApprovalModes         map[string]ApprovalMode
	DefaultToolsApprovalMode  ApprovalMode
	Auth                      AuthMode
	Scopes                    []string
	OAuthClientID             string
	OAuthResource             string
	EnvironmentID             string
}

// GlobalConfig is the top-level (not per-server) MCP config surface (§1
// "Global config").
type GlobalConfig struct {
	// CredentialsStore: auto|file|keyring. Default "auto".
	CredentialsStore  string
	OAuthCallbackPort int
	OAuthCallbackURL  string
}

// DefaultGlobalConfig returns the spec defaults: auto store, loopback
// callback (port 0 = OS-assigned, per §3's "127.0.0.1:0").
func DefaultGlobalConfig() GlobalConfig {
	return GlobalConfig{
		CredentialsStore:  "auto",
		OAuthCallbackPort: 0,
		OAuthCallbackURL:  "",
	}
}

// ParseServerConfig builds a ServerConfig from a generic, already-decoded
// map (the shape both a JSON .mcp.json entry and a TOML
// [mcp_servers.<name>] table decode to). Format-agnostic by design: this
// unit ships the JSON-loading path (LoadServersJSON, the Claude-Code-compat
// target §1 names explicitly); a TOML loader is deferred to whichever unit
// wires agora's own config file, and can reuse this function unchanged
// once it decodes TOML into the same map[string]any shape.
func ParseServerConfig(name string, raw map[string]any) (ServerConfig, error) {
	c := ServerConfig{
		Name:                     name,
		Enabled:                  true,
		Required:                 false,
		StartupTimeout:           30 * time.Second,
		ToolTimeout:              300 * time.Second,
		DefaultToolsApprovalMode: ApprovalAuto,
		Auth:                     AuthOAuth,
		EnvironmentID:            "local",
	}

	if v, ok := raw["enabled"].(bool); ok {
		c.Enabled = v
	}
	if v, ok := raw["required"].(bool); ok {
		c.Required = v
	}
	if v, ok := rawString(raw, "environment_id"); ok {
		c.EnvironmentID = v
	}
	if v, ok := raw["supports_parallel_tool_calls"].(bool); ok {
		c.SupportsParallelToolCalls = v
	}

	// Transport detection: exactly one of command/url/module.
	_, hasCmd := raw["command"]
	_, hasURL := raw["url"]
	_, hasModule := raw["module"]
	n := 0
	for _, has := range []bool{hasCmd, hasURL, hasModule} {
		if has {
			n++
		}
	}
	if n > 1 {
		return ServerConfig{}, fmt.Errorf("mcp: server %q: %w", name, ErrMixedTransport)
	}
	switch {
	case hasCmd:
		c.Transport = TransportStdio
		cmd, _ := rawString(raw, "command")
		if strings.TrimSpace(cmd) == "" {
			return ServerConfig{}, fmt.Errorf("mcp: server %q: %w", name, ErrMissingCommand)
		}
		c.Command = cmd
		c.Args = rawStringSlice(raw, "args")
		c.Env = rawStringMap(raw, "env")
		c.EnvVars = parseEnvVars(raw["env_vars"])
		c.Cwd, _ = rawString(raw, "cwd")
	case hasURL:
		c.Transport = TransportHTTP
		u, _ := rawString(raw, "url")
		if strings.TrimSpace(u) == "" {
			return ServerConfig{}, fmt.Errorf("mcp: server %q: %w", name, ErrMissingURL)
		}
		c.URL = u
		c.BearerTokenEnvVar, _ = rawString(raw, "bearer_token_env_var")
		c.HTTPHeaders = rawStringMap(raw, "http_headers")
		c.EnvHTTPHeaders = rawStringMap(raw, "env_http_headers")
		if _, hasRaw := raw["bearer_token"]; hasRaw && c.Enabled {
			return ServerConfig{}, fmt.Errorf("mcp: server %q: %w", name, ErrRawBearerToken)
		}
	case hasModule:
		c.Transport = TransportWasm
		c.Module, _ = rawString(raw, "module")
		c.ModuleHash, _ = rawString(raw, "module_hash")
		if c.Enabled {
			return ServerConfig{}, fmt.Errorf("mcp: server %q: %w", name, ErrWasmUnsupported)
		}
	default:
		if c.Enabled {
			return ServerConfig{}, fmt.Errorf("mcp: server %q: %w", name, ErrNoTransport)
		}
	}

	// startup_timeout_sec / startup_timeout_ms — sec wins if both (§1).
	if ms, ok := rawNumber(raw, "startup_timeout_ms"); ok {
		c.StartupTimeout = time.Duration(ms) * time.Millisecond
	}
	if sec, ok := rawNumber(raw, "startup_timeout_sec"); ok {
		c.StartupTimeout = time.Duration(sec*1000) * time.Millisecond
	}
	if sec, ok := rawNumber(raw, "tool_timeout_sec"); ok {
		c.ToolTimeout = time.Duration(sec*1000) * time.Millisecond
	}

	c.EnabledTools = rawStringSlice(raw, "enabled_tools")
	c.DisabledTools = rawStringSlice(raw, "disabled_tools")

	if tm, ok := raw["tools"].(map[string]any); ok {
		c.ToolApprovalModes = map[string]ApprovalMode{}
		for tool, v := range tm {
			if tv, ok := v.(map[string]any); ok {
				if mode, ok := rawString(tv, "approval_mode"); ok {
					c.ToolApprovalModes[tool] = ApprovalMode(mode)
				}
			}
		}
	}
	if v, ok := rawString(raw, "default_tools_approval_mode"); ok {
		c.DefaultToolsApprovalMode = ApprovalMode(v)
	}
	if v, ok := rawString(raw, "auth"); ok {
		c.Auth = AuthMode(v)
	}
	c.Scopes = rawStringSlice(raw, "scopes")
	if oa, ok := raw["oauth"].(map[string]any); ok {
		c.OAuthClientID, _ = rawString(oa, "client_id")
	}
	c.OAuthResource, _ = rawString(raw, "oauth_resource")

	return c, nil
}

// ParseServers parses a keyed table of server configs (the whole
// mcpServers/mcp_servers map).
func ParseServers(table map[string]map[string]any) (map[string]ServerConfig, error) {
	out := make(map[string]ServerConfig, len(table))
	for name, raw := range table {
		c, err := ParseServerConfig(name, raw)
		if err != nil {
			return nil, err
		}
		out[name] = c
	}
	return out, nil
}

// SortedNames returns server names in deterministic (sorted) order — every
// place this package iterates a server/tool map for output must go through
// a sort, never raw map-range order (house style, ground rule 3).
func SortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Interpolate substitutes {identity} / {identity.<field>} in s from id
// (§1's identity-interpolation extension). Unknown {identity.X} fields
// substitute to "" rather than erroring — a config typo should not crash
// startup for every server sharing the file; keep the token intact only for
// {identity} itself when the intent is ambiguous is not needed here since
// {identity} always resolves (ID is never empty for a resolved identity).
func Interpolate(s string, id contracts.Identity) string {
	replacer := strings.NewReplacer(
		"{identity.id}", id.ID,
		"{identity.fingerprint}", id.Fingerprint,
		"{identity.kind}", string(id.Kind),
		"{identity.display_name}", id.DisplayName,
		"{identity.source}", id.Source,
		"{identity}", id.ID,
	)
	return replacer.Replace(s)
}

// InterpolateConfig returns a copy of c with {identity} substitutions
// applied to every field §1 names (args, env values, url, http_headers,
// cwd). Env VAR NAMES and env_vars entries are not interpolated (only
// values/URLs/paths are, per §1); this matters for the catalog cache
// fingerprint (§2), which hashes the config AFTER interpolation so two
// identities sharing one stanza get distinct cache entries.
func InterpolateConfig(c ServerConfig, id contracts.Identity) ServerConfig {
	out := c
	out.Args = interpolateSlice(c.Args, id)
	out.Env = interpolateMap(c.Env, id)
	out.URL = Interpolate(c.URL, id)
	out.HTTPHeaders = interpolateMap(c.HTTPHeaders, id)
	out.Cwd = Interpolate(c.Cwd, id)
	return out
}

func interpolateSlice(ss []string, id contracts.Identity) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = Interpolate(s, id)
	}
	return out
}

func interpolateMap(m map[string]string, id contracts.Identity) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = Interpolate(v, id)
	}
	return out
}

// ToolAllowed applies the §1 filter rule: allowed iff
// (enabled_tools is unset || contains) && !disabled_tools.contains.
func (c ServerConfig) ToolAllowed(tool string) bool {
	if len(c.EnabledTools) > 0 && !contains(c.EnabledTools, tool) {
		return false
	}
	if contains(c.DisabledTools, tool) {
		return false
	}
	return true
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func parseEnvVars(v any) []EnvVarRef {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]EnvVarRef, 0, len(arr))
	for _, e := range arr {
		switch t := e.(type) {
		case string:
			out = append(out, EnvVarRef{Name: t, Source: "local"})
		case map[string]any:
			ref := EnvVarRef{Source: "local"}
			if n, ok := rawString(t, "name"); ok {
				ref.Name = n
			}
			if s, ok := rawString(t, "source"); ok {
				ref.Source = s
			}
			out = append(out, ref)
		}
	}
	return out
}

func rawString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func rawNumber(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func rawStringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func rawStringMap(m map[string]any, key string) map[string]string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(obj))
	for k, e := range obj {
		if s, ok := e.(string); ok {
			out[k] = s
		}
	}
	return out
}
