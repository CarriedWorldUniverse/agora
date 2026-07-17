package toolrunner

import (
	"context"
	"encoding/json"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// MCPSource is the narrow seam Surface folds MCP tools through (U-B3).
// Deliberately independent of internal/mcp.Manager's current shape: as of
// this build, internal/mcp.Manager/Client expose ListTools (via Client) but
// no tool-INVOCATION method yet — the Client interface (internal/mcp/
// manager.go) is `{ListTools, Close}` only, no Call. So there is no
// existing "Manager.Call" this package could bind to today. This interface
// is what a later-phase adapter over Manager+Client (once it grows a
// call path) implements; tests here fake it directly and never import
// internal/mcp, per the brief ("do NOT import heavyweight MCP machinery
// into your tests").
type MCPSource interface {
	// Tools returns the current aggregated MCP tool catalog as ToolSpecs,
	// already mcp__<server>__<tool>-qualified (internal/mcp/naming.go).
	Tools(ctx context.Context) ([]contracts.ToolSpec, error)
	// Call invokes an mcp__-qualified tool by its qualified name.
	Call(ctx context.Context, name string, args json.RawMessage) (Result, error)
}
