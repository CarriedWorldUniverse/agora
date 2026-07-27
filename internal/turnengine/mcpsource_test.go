package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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

// failingMCPSource always fails Tools() — simulates a required MCP server
// that couldn't start.
type failingMCPSource struct{ err error }

func (f *failingMCPSource) Tools(ctx context.Context) ([]contracts.ToolSpec, error) {
	return nil, f.err
}
func (f *failingMCPSource) Call(ctx context.Context, name string, args json.RawMessage) (toolrunner.Result, error) {
	return toolrunner.Result{}, f.err
}

// TestManager_MCPSource_SpecsError_SurfacesMessageBeforeFailing:
// adversarial review of PR #94, finding 2 — a broken MCP source must not
// fail the turn silently; the real error reaches an EvError event before
// the terminal EvTurnFailed, and the model is never even invoked (Specs()
// is resolved before the request is built).
func TestManager_MCPSource_SpecsError_SurfacesMessageBeforeFailing(t *testing.T) {
	roots := managerTestRoots(t)
	src := &failingMCPSource{err: fmt.Errorf("mcp: required server(s) failed to start")}
	provider := fake.NewProvider(fake.Step{Text: "should never be reached"})
	m := NewManager("th_mcp_broken", provider, WithRoots(roots), WithMCPSource(src),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "go"}

	var sawError, sawFailed bool
	deadline := time.After(testTimeout)
	for !sawFailed {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for turn.failed")
		case ev, ok := <-out:
			if !ok {
				t.Fatal("out closed before turn.failed")
			}
			switch ev.Type {
			case contracts.EvError:
				var p struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(ev.Payload, &p)
				if !strings.Contains(p.Message, "required server(s) failed to start") {
					t.Fatalf("EvError message = %q; want the real mcp error", p.Message)
				}
				sawError = true
			case contracts.EvTurnFailed:
				sawFailed = true
			}
		}
	}
	if !sawError {
		t.Fatal("turn failed with no EvError carrying the real reason — same silent-failure gap the fix closes")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	<-runErr
	if provider.StepsRemaining() != 1 {
		t.Fatalf("provider StepsRemaining = %d; want 1 (untouched — Specs() must fail BEFORE the model is ever called)", provider.StepsRemaining())
	}
}

// closeTrackingMCPSource records whether/how-many-times Close was called.
type closeTrackingMCPSource struct {
	closes int
}

func (c *closeTrackingMCPSource) Tools(ctx context.Context) ([]contracts.ToolSpec, error) {
	return nil, nil
}
func (c *closeTrackingMCPSource) Call(ctx context.Context, name string, args json.RawMessage) (toolrunner.Result, error) {
	return toolrunner.Result{}, nil
}
func (c *closeTrackingMCPSource) Close() { c.closes++ }

// TestManager_MCPSource_ClosedWhenRunEnds: adversarial review of PR #94,
// finding 3 — an MCP source implementing Close() is torn down when the
// Manager's Run loop ends (the one point both the TUI/pipe lane and the
// daemon's per-thread engine already drive to completion), not leaked.
func TestManager_MCPSource_ClosedWhenRunEnds(t *testing.T) {
	roots := managerTestRoots(t)
	src := &closeTrackingMCPSource{}
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_mcp_close", provider, WithRoots(roots), WithMCPSource(src),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	if src.closes != 0 {
		t.Fatalf("Close called %d times before Run even ended", src.closes)
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if src.closes != 1 {
		t.Fatalf("Close called %d times after Run ended; want exactly 1", src.closes)
	}
}

// TestManager_MCPSource_NilSource_CloseIsNoop: a nil mcpSource (the
// default) must not panic when Run tears down — the type assertion on a
// nil interface value safely fails rather than matching.
func TestManager_MCPSource_NilSource_CloseIsNoop(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_mcp_nilclose", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	drainToTurnCompleted(t, out, testTimeout)
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run panicked or errored on nil mcpSource teardown: %v", err)
	}
}
