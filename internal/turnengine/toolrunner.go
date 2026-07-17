package turnengine

import (
	"context"
	"encoding/json"
	"fmt"

	bridle "github.com/CarriedWorldUniverse/bridle"
)

// noopToolRunner is the ToolRunner this slice passes to Harness.RunTurn.
// TurnRequest.Tools is always empty this slice (no tool surface — that's
// U-B1/U-C2's job) and the fake provider never issues a ToolCall, so this
// implementation is never expected to be invoked in practice; it errors
// loudly rather than silently fabricating a result if that assumption
// ever breaks.
type noopToolRunner struct{}

func (noopToolRunner) Run(_ context.Context, call bridle.ToolCall) (json.RawMessage, error) {
	return nil, fmt.Errorf("turnengine: no tool surface this slice (U-C2/U-B1) — unexpected call to %q", call.Name)
}
