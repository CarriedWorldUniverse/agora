package toolrunner

import (
	"context"
	"encoding/json"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Call is one model tool invocation: the bare (or mcp__-qualified) name plus
// its raw JSON arguments.
type Call struct {
	Name string
	Args json.RawMessage
}

// Result is one tool's outcome. Phase 2 marshals this to the
// json.RawMessage shape the turn engine sends back to the model — kept
// trivially adaptable (a single string body + an error flag) rather than
// pre-guessing that wire shape here.
type Result struct {
	Content string
	IsError bool
}

// errorResultf builds an IsError Result from an error, never panicking the
// caller — the "unknown name -> a clean error Result, never panic" rule.
func errorResult(err error) Result {
	return Result{Content: err.Error(), IsError: true}
}

// Family is one pluggable native tool family (agora-spec-mcp.md §5a: fs,
// exec, web, browser, computer — a profile enables a subset). Name is the
// contracts.Family* constant (e.g. contracts.FamilyFS).
type Family interface {
	Name() string
	Specs() []contracts.ToolSpec
	Handles(name string) bool
	Execute(ctx context.Context, call Call) (Result, error)
}
