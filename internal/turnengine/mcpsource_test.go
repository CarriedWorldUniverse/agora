package turnengine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// fakeMCPSource is a toolrunner.MCPSource with one mcp__demo__ping tool.
type fakeMCPSource struct{ called bool }

func (f *fakeMCPSource) Tools(ctx context.Context) ([]contracts.ToolSpec, error) {
	return []contracts.ToolSpec{{Name: "mcp__demo__ping", Description: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (f *fakeMCPSource) Call(ctx context.Context, name string, args json.RawMessage) (toolrunner.Result, error) {
	f.called = true
	return toolrunner.Result{Content: "pong from " + name}, nil
}

// TestManager_MCPSource_ToolGatedAndRouted: an MCP tool appears in the
// surface, classifies as mcp_tool (prompts under promptAllPolicy), and on
// approve executes through the source.
func TestManager_MCPSource_ToolGatedAndRouted(t *testing.T) {
	roots := managerTestRoots(t)
	src := &fakeMCPSource{}
	pingArgs, _ := json.Marshal(map[string]any{})
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{{ID: "1", Name: "mcp__demo__ping", Args: pingArgs}}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_mcp", provider, WithRoots(roots), WithMCPSource(src),
		WithPolicy(promptAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 2)
	out := make(chan contracts.Event, 64)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "ping it"}

	req := recvApprovalRequested(t, out, testTimeout)
	var ar contracts.ApprovalRequest
	_ = json.Unmarshal(req.Payload, &ar)
	if ar.Kind != contracts.KindMCPTool {
		t.Fatalf("kind = %q; want mcp_tool", ar.Kind)
	}
	in <- contracts.Input{Type: contracts.InApprovalResponse, ID: ar.ID, Decision: contracts.DecisionAllow, Scope: contracts.ScopeOnce}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !src.called {
		t.Fatal("MCP source Call was never invoked — the tool didn't route")
	}
}

// TestManager_MCPSource_Nil_NoBehaviorChange: a Manager built without
// WithMCPSource has no mcp__-prefixed tools available — unaffected by this
// unit for callers that don't opt in.
func TestManager_MCPSource_Nil_NoBehaviorChange(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_nomcp", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
