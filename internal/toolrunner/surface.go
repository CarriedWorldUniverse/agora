package toolrunner

import (
	"context"
	"fmt"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Surface merges N native tool Families plus an optional MCPSource into one
// []contracts.ToolSpec and dispatches Execute(ctx, Call) by name: a
// mcp__-prefixed name routes to the MCPSource; anything else routes to the
// first Family whose Handles(name) matches (families are expected to
// register disjoint name sets — first match wins if they don't). An
// unrecognized name never panics: it returns a clean IsError Result.
type Surface struct {
	families []Family
	mcp      MCPSource
}

// NewSurface builds a Surface over families. mcp may be nil (no MCP
// servers configured/folded in) — Specs/Execute simply skip it.
func NewSurface(mcp MCPSource, families ...Family) *Surface {
	return &Surface{families: families, mcp: mcp}
}

// Specs returns every family's specs (in registration order) followed by
// the MCPSource's tools, if any. Returns an error only if the MCPSource's
// listing itself fails — native family specs are always static/in-memory
// and cannot fail.
func (s *Surface) Specs(ctx context.Context) ([]contracts.ToolSpec, error) {
	var out []contracts.ToolSpec
	for _, f := range s.families {
		out = append(out, f.Specs()...)
	}
	if s.mcp != nil {
		mcpSpecs, err := s.mcp.Tools(ctx)
		if err != nil {
			return nil, fmt.Errorf("toolrunner: listing mcp tools: %w", err)
		}
		out = append(out, mcpSpecs...)
	}
	return out, nil
}

// Execute dispatches call by name. A Go error return is reserved for the
// harness itself misbehaving; an unresolvable name, bad args, or any
// tool-level failure comes back as Result{IsError: true}, never a panic
// and never (for those cases) a Go error.
func (s *Surface) Execute(ctx context.Context, call Call) (Result, error) {
	if strings.HasPrefix(call.Name, mcpPrefix) {
		if s.mcp == nil {
			return errorResult(fmt.Errorf("%w: %s (no mcp source configured)", ErrUnknownTool, call.Name)), nil
		}
		return s.mcp.Call(ctx, call.Name, call.Args)
	}
	for _, f := range s.families {
		if f.Handles(call.Name) {
			return f.Execute(ctx, call)
		}
	}
	return errorResult(fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)), nil
}
