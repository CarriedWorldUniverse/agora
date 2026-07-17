package subagent

import (
	"context"
	"encoding/json"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// AgentRunner executes one agent turn/session — the turn-engine's seam.
// Ground rule 6: agent EXECUTION (model calls, the tool loop) is out of
// this unit's scope; the Manager only knows how to invoke this interface
// and interpret its result. Production wiring plugs in the real
// turn-engine; tests use a deterministic stub (no model calls).
type AgentRunner interface {
	// Run executes one attempt of an agent invocation and returns its
	// result. Run must respect ctx cancellation promptly (spec §2a:
	// "in-flight tool calls abort").
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// RunRequest is what the Manager hands the runner for one attempt.
type RunRequest struct {
	AgentID      string
	ParentThread string
	AgentType    string
	// Prompt is the contract — spec §2: "the prompt is the contract"
	// (no conversation history by default). For a continuation, Prompt is
	// the new message; the runner is expected to know (out of this
	// package's scope) how to load the child thread's prior context.
	Prompt string
	Model  string
	Effort contracts.Effort
	Tools  []string
	// Schema forces structured output when non-nil (spec §2: "child must
	// call a StructuredOutput tool matching schema").
	Schema json.RawMessage
	// Attempt is 0 on the first try, incremented on each schema-mismatch
	// retry (spec §2: "validated, retried on mismatch").
	Attempt int
	// Feedback carries the previous attempt's validation failure back to
	// the runner so it can correct course (empty on Attempt 0).
	Feedback string
}

// RunResult is one attempt's outcome.
type RunResult struct {
	// Output is the child's final message — spec §2: "the child's final
	// message IS the return value ... they return raw data, not prose".
	// When Schema was set, Output is the StructuredOutput payload.
	Output json.RawMessage
	// Question is set when the child ends with a question-shaped result
	// instead of a normal completion (spec §2: "question bubbling").
	Question *contracts.QuestionAsked
}
