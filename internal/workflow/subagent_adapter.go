package workflow

import (
	"context"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/internal/subagent"
)

// SubagentInvoker maps AgentInvoker onto a real *subagent.Manager (U10) —
// the production wiring for ctx.agent() (spec §2: "Every ctx.agent() IS a
// real subagent"). ParentThread is the workflow run's own thread id
// (RunOptions.ThreadID), registered with the Manager via RegisterRoot by
// the caller before a run starts (subagent.Manager's own fail-closed
// contract — see manager.go's Spawn doc).
type SubagentInvoker struct {
	Manager      *subagent.Manager
	ParentThread string
}

var _ AgentInvoker = (*SubagentInvoker)(nil)

// InvokeAgent spawns a child via the Manager and blocks for its result —
// Foreground: true is set because, from the workflow's perspective, this
// call IS synchronous (ctx.agent blocks the starlark green-thread); the
// Manager's own async dispatch happens underneath regardless, so this only
// affects the §2a cancellation-matrix bit recorded on the graph edge, which
// is the correct bit for a call the workflow itself is waiting on.
func (s *SubagentInvoker) InvokeAgent(ctx context.Context, prompt string, opts AgentCallOpts) (AgentCallResult, error) {
	id, err := s.Manager.Spawn(ctx, s.ParentThread, prompt, subagent.SpawnOpts{
		AgentType:  opts.AgentType,
		Model:      opts.Model,
		Effort:     opts.Effort,
		Foreground: true,
		Isolation:  opts.Isolation,
		Schema:     opts.Schema,
	})
	if err != nil {
		return AgentCallResult{}, fmt.Errorf("workflow: spawn agent: %w", err)
	}
	res, runErr, ok := s.Manager.Result(id)
	if !ok {
		return AgentCallResult{}, fmt.Errorf("workflow: agent %s vanished from the manager before a result was available", id)
	}
	if runErr != nil {
		return AgentCallResult{}, runErr
	}
	return AgentCallResult{Output: res.Output, Question: res.Question}, nil
}
