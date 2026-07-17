package mcp

import "errors"

// Sentinel errors. Config/startup errors are fail-closed by construction
// (ground rule 6): a misconfigured or auth-failing server never silently
// becomes "trusted", it returns one of these.
var (
	// ErrMixedTransport: more than one of command/url/module set (§1: "mixing
	// transport fields = hard error"). module (wasm, §1a) is out of scope for
	// this unit but still detected here so a config that names it fails
	// closed rather than silently ignoring it.
	ErrMixedTransport = errors.New("mcp: config sets more than one transport field (command/url/module)")
	// ErrNoTransport: none of command/url/module set on an enabled server.
	ErrNoTransport = errors.New("mcp: config sets no transport field (command/url/module)")
	// ErrWasmUnsupported: module transport is §1a (v1.1), not built by U8.
	ErrWasmUnsupported = errors.New("mcp: wasm transport (module) is not implemented by this build")
	// ErrMissingCommand: stdio transport without a command.
	ErrMissingCommand = errors.New("mcp: stdio transport requires \"command\"")
	// ErrMissingURL: streamable_http transport without a url.
	ErrMissingURL = errors.New("mcp: streamable_http transport requires \"url\"")
	// ErrRawBearerToken: config supplied a literal bearer_token, which §1
	// forbids ("never accept a raw bearer_token literal in config").
	ErrRawBearerToken = errors.New("mcp: raw \"bearer_token\" literal is not accepted in config, use bearer_token_env_var")

	// ErrRequiredServerFailed: a required=true server failed to start; the
	// manager aggregates all such failures into one error (§2).
	ErrRequiredServerFailed = errors.New("mcp: required server(s) failed to start")
	// ErrStartupTimeout: a server did not become Ready within its
	// startup_timeout_sec.
	ErrStartupTimeout = errors.New("mcp: server startup timed out")
	// ErrAuthRequired: startup failed because the server needs interactive
	// auth (§2's "special-case auth-required" UX).
	ErrAuthRequired = errors.New("mcp: server requires auth, run agora mcp login <server>")

	// ErrToolFiltered: a call was rejected because the tool is not in the
	// effective allow-list (enabled_tools/disabled_tools, §1 filter rule).
	ErrToolFiltered = errors.New("mcp: tool is filtered out for this server")
	// ErrToolNotFound: tool_search/select referenced a name the aggregated
	// catalog does not contain.
	ErrToolNotFound = errors.New("mcp: tool not found in catalog")

	// ErrOAuthNoCredential: no stored/derivable credential and no discovery
	// metadata reachable — Unsupported per §3's auth-status order.
	ErrOAuthNoCredential = errors.New("mcp: oauth unsupported, no credential and no discoverable metadata")
	// ErrOAuthReauth: stored credential exists but is unrefreshable
	// (LoggedOut(reauth) per §3).
	ErrOAuthReauth = errors.New("mcp: oauth credential expired and unrefreshable, reauth required")
	// ErrOAuthLoginRequired: no stored credential but OAuth is discoverable
	// (LoggedOut(login) per §3).
	ErrOAuthLoginRequired = errors.New("mcp: oauth login required")
	// ErrOAuthLoginTimeout: the loopback login flow exceeded its 300s budget.
	ErrOAuthLoginTimeout = errors.New("mcp: oauth login flow timed out")
)
