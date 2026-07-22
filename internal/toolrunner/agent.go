package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
)

// ToolAgent is the agent family's one tool (agora-spec-subagents.md §2:
// "One tool, not a namespace of five").
const ToolAgent = "agent"

// AgentFamily is the native tool family exposing the agent() spawn tool.
//
// v1 scope (this unit): SYNCHRONOUS fire-and-collect only. Execute blocks
// until the spawned child finishes (subagent.Manager.Spawn with
// Foreground:true, then Result) and returns the child's final message as
// the tool result — the spec's run_in_background default, send_message
// continuation, question-bubbling-as-a-real-channel, and isolation are
// documented cuts (see doc.go/the unit's build report), not silently
// dropped behavior: agora-spec-subagents.md §2's background+notification
// path and §2's send_message are explicitly OUT of this unit's scope.
type AgentFamily struct {
	manager      *subagent.Manager
	parentThread string
}

// NewAgentFamily builds the agent family bound to one parent thread. A
// turnengine.Manager for thread X constructs at most one AgentFamily,
// closed over X — every agent() call from X's turns records X as the
// spawn's parent (subagent-spec §3's parent_thread).
func NewAgentFamily(manager *subagent.Manager, parentThread string) *AgentFamily {
	return &AgentFamily{manager: manager, parentThread: parentThread}
}

func (a *AgentFamily) Name() string { return contracts.FamilyAgent }

func (a *AgentFamily) Handles(name string) bool { return name == ToolAgent }

func (a *AgentFamily) Specs() []contracts.ToolSpec {
	return []contracts.ToolSpec{
		{
			Name: ToolAgent,
			Description: "Spawn a subagent to complete a delegated task in its own context window. " +
				"The subagent's final message is returned as this call's result — write prompt as a " +
				"self-contained task description; the subagent has no access to this conversation's " +
				"history (agora-spec-subagents.md §2: \"the prompt is the contract\").",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt":     map[string]any{"type": "string", "description": "The task for the subagent, as a self-contained instruction — it sees nothing else."},
					"agent_type": map[string]any{"type": "string", "description": "Which agent definition to use (default: general-purpose)."},
				},
				"required": []string{"prompt"},
			}),
		},
	}
}

// agentArgs is the agent tool's argument shape — the minimal subset of
// agora-spec-subagents.md §2's opts this unit implements (prompt +
// agent_type; model/effort/run_in_background/isolation/schema overrides
// are a later unit — see AgentFamily's doc comment).
type agentArgs struct {
	Prompt    string `json:"prompt"`
	AgentType string `json:"agent_type"`
}

func (a *AgentFamily) Execute(ctx context.Context, call Call) (Result, error) {
	if call.Name != ToolAgent {
		return errorResult(fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)), nil
	}
	var args agentArgs
	if err := json.Unmarshal(call.Args, &args); err != nil || args.Prompt == "" {
		return errorResult(fmt.Errorf("%w: agent", ErrBadArgs)), nil
	}

	agentID, err := a.manager.Spawn(ctx, a.parentThread, args.Prompt, subagent.SpawnOpts{
		AgentType: args.AgentType,
		// Foreground:true — v1 is synchronous fire-and-collect (see the
		// package doc comment): the caller blocks this turn's tool call on
		// the child's completion rather than returning agent_id immediately.
		Foreground: true,
	})
	if err != nil {
		// Depth cap / spawn cap / thread-create failure — a tool error the
		// model can see and react to, never a Go error (house rule: a
		// tool-level failure is Result{IsError:true}, not an error return).
		return Result{Content: fmt.Sprintf("agent spawn failed: %v", err), IsError: true}, nil
	}

	res, runErr, ok := a.manager.Result(agentID)
	if !ok {
		// Defensive only: Result returning ok=false right after a
		// successful Spawn recorded agentID would mean the Manager lost its
		// own bookkeeping — should not happen, but never crash the parent
		// turn over it.
		return Result{Content: fmt.Sprintf("agent %s: lost track of the spawned agent", agentID), IsError: true}, nil
	}
	if runErr != nil {
		return Result{Content: fmt.Sprintf("agent %s failed: %v", agentID, runErr), IsError: true}, nil
	}
	if res.Question != nil {
		// Spec §2 "question bubbling" is a later unit (no real channel to
		// the parent's own context exists yet) — surface the fact rather
		// than silently discarding it.
		return Result{Content: fmt.Sprintf("agent %s ended with a question (id=%s); bubbling to the parent is not implemented in v1", agentID, res.Question.ID)}, nil
	}

	return Result{Content: decodeAgentOutput(res.Output)}, nil
}

// decodeAgentOutput renders a subagent.RunResult.Output for the tool
// result's plain-text Content: the AgentRunner this unit ships
// (internal/subagent/enginerunner) encodes a non-schema child's final
// message as a bare JSON string, which decodes cleanly here to its raw
// text; a schema-forced structured payload (JSON object) is passed through
// as its raw JSON verbatim instead.
func decodeAgentOutput(output json.RawMessage) string {
	var s string
	if err := json.Unmarshal(output, &s); err == nil {
		return s
	}
	return string(output)
}
