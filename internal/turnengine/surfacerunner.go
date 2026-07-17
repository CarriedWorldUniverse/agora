package turnengine

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// toolDefsFromSpecs maps toolrunner's model-visible tool catalog onto
// bridle's TurnRequest.Tools shape — the exact same 3 fields
// (Name/Description/InputSchema), a straight copy, per the brief.
func toolDefsFromSpecs(specs []contracts.ToolSpec) []bridle.ToolDef {
	if len(specs) == 0 {
		return nil
	}
	defs := make([]bridle.ToolDef, len(specs))
	for i, s := range specs {
		defs[i] = bridle.ToolDef{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		}
	}
	return defs
}

// surfaceRunner implements bridle.ToolRunner over a *toolrunner.Surface —
// the merged Phase 1 fs/exec (+ MCP, later) tool surface. This is Phase 2
// U-C2: a turn's tool calls now actually EXECUTE, agora-side, instead of
// hitting noopToolRunner's "no tool surface this slice" error.
//
// U-C3: tool execution here is UNGATED — no BeforeToolCall approval hook
// is wired yet (that lands in U-C3, the next unit, before ANY real-Claude
// wiring in U-F1). The fake provider is the only thing that can drive a
// ToolCall through this runner today (NewManager's provider seam only ever
// takes bridle/fake.NewProvider(...) in tests until claudesdk lands), so
// there is no real-world risk from the missing gate yet — but a
// BeforeToolCall hook belongs on bridle.Harness (via hooks.go), not inside
// this runner, so U-C3's work is wiring that hook, not touching this file.
type surfaceRunner struct {
	surface *toolrunner.Surface
}

func newSurfaceRunner(surface *toolrunner.Surface) *surfaceRunner {
	return &surfaceRunner{surface: surface}
}

// Run dispatches call through the Surface and maps toolrunner's (Result,
// error) return onto bridle's (json.RawMessage, error) ToolRunner contract.
//
// The mapping (read against bridle's run.go executeToolCall before
// changing this):
//
//   - A genuine Surface-level Go error (per toolrunner.Surface.Execute's own
//     doc: "reserved for the harness itself misbehaving" — e.g. a future
//     MCPSource's Call failing; the fs/exec families never return one) is
//     passed straight through as a Go error from Run.
//   - A tool that ran and reported a TOOL-level failure (Result.IsError —
//     bad args, a path-escape/protected-path guard, an unresolvable/unknown
//     tool name per Surface.Execute's own "clean IsError Result, never a
//     Go error" contract) is ALSO turned into a Go error from Run, built
//     from Result.Content.
//
// Both of those look like "the same case" from outside this function, and
// that is deliberate: bridle's executeToolCall (run.go) captures ANY error
// ToolRunner.Run returns onto ToolCallResult.Err and folds it into the
// tool_result message the model sees next ("error: <msg>") — it does NOT
// treat a ToolRunner.Run error as turn-aborting. (Turn aborts come only
// from a BeforeToolCall/AfterToolCall HOOK returning an error/HookAbort —
// this runner never touches those.) So returning a Go error here is always
// safe: it is bridle's documented way for "the model should see the error
// string and decide what to do", never a turn-ending fault. Only a
// SUCCESSFUL Result (IsError false) is marshaled and returned as the
// tool_result payload the model sees as ordinary content.
func (r *surfaceRunner) Run(ctx context.Context, call bridle.ToolCall) (json.RawMessage, error) {
	result, err := r.surface.Execute(ctx, toolrunner.Call{Name: call.Name, Args: call.Args})
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return nil, errors.New(result.Content)
	}
	// json.Marshal, not a raw byte cast: bridle expects Run's return to be
	// a valid JSON value (see bridle/fake.ToolResult usage — a result of
	// `"echoed"` is a JSON-encoded string, not the bare bytes echoed), and
	// Result.Content is a plain Go string, not pre-encoded JSON.
	return json.Marshal(result.Content)
}
