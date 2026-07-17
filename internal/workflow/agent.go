package workflow

import (
	"context"
	"encoding/json"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// AgentCallOpts is the resolved, engine-facing shape of one ctx.agent()
// call — spec §2: "ctx.agent(prompt, label=?, phase=?, agent_type=?,
// model=?, effort=?, schema=?, isolation=?)". Model/Effort have already
// been folded through the §2a resolution order up to (and including) the
// phase-defaults layer by the time an AgentInvoker sees them; the
// invoker/its subagent.Manager still applies the remaining layers (agent
// def, parent inheritance) per subagent.ResolveInheritance.
type AgentCallOpts struct {
	Label     string
	Phase     string
	AgentType string
	Model     string
	Effort    contracts.Effort
	Schema    json.RawMessage
	Isolation string
}

// AgentCallResult is what one ctx.agent() invocation produced.
type AgentCallResult struct {
	// Output is the child's final message/structured payload — nil when the
	// agent died/was skipped (spec §2: "Returns None if the agent
	// dies/skipped — callers filter").
	Output json.RawMessage
	// Question is set when the child's result is a bubbled question
	// instead of a normal completion (spec §2, §2 "Questions raised by
	// agents inside a stage bubble to the engine first").
	Question *contracts.QuestionAsked
}

// AgentInvoker is the seam onto the real subagent primitive — spec §2:
// "Every ctx.agent() IS a real subagent ... The workflow engine is
// orchestration ONLY — it owns no execution machinery of its own." Tests
// use a deterministic fake; production wiring plugs in SubagentInvoker
// (subagent_adapter.go), which maps onto *subagent.Manager (U10).
type AgentInvoker interface {
	// InvokeAgent blocks until the child finishes (spec §2: ctx.agent
	// "blocks the starlark green-thread until done").
	InvokeAgent(ctx context.Context, prompt string, opts AgentCallOpts) (AgentCallResult, error)
}
