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

func taskWriteCall(id string, tasks []map[string]string) bridle.ToolInvocation {
	args, _ := json.Marshal(map[string]any{"tasks": tasks})
	return bridle.ToolInvocation{ID: id, Name: contracts.ToolTaskWrite, Args: args}
}

// The task family must be reachable through a real Manager, not merely
// correct in its own unit tests — #100 was exactly the bug where a
// fully-tested subsystem was never wired into production.
func TestManager_TaskTools_AreAdvertisedAndExecute(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			taskWriteCall("1", []map[string]string{
				{"content": "step one", "status": "completed"},
				{"content": "step two", "status": "in_progress"},
			}),
		}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_tasks", provider, WithRoots(roots),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	// Advertised to the model.
	specs, err := m.surface.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs: %v", err)
	}
	var sawWrite, sawRead bool
	for _, s := range specs {
		switch s.Name {
		case contracts.ToolTaskWrite:
			sawWrite = true
		case contracts.ToolTaskRead:
			sawRead = true
		}
	}
	if !sawWrite || !sawRead {
		t.Fatalf("task tools not advertised (write=%v read=%v) — the family is not wired into the surface",
			sawWrite, sawRead)
	}

	// And actually executes: under the default sandbox-auto policy a
	// KindRead call must run without prompting.
	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 64)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "track this work"}
	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; want turn.completed with no approval prompt", got)
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// Each Manager owns its own list: a subagent's checklist must not overwrite
// its parent's.
func TestManager_TaskLists_AreNotSharedBetweenManagers(t *testing.T) {
	roots := managerTestRoots(t)
	newFam := func() *toolrunner.TaskFamily { return toolrunner.NewTaskFamily() }

	a, b := newFam(), newFam()
	args, _ := json.Marshal(map[string]any{
		"tasks": []map[string]string{{"content": "only in a", "status": "pending"}},
	})
	if _, err := a.Execute(context.Background(),
		toolrunner.Call{Name: contracts.ToolTaskWrite, Args: args}); err != nil {
		t.Fatal(err)
	}
	if len(b.Tasks()) != 0 {
		t.Fatal("two task families share state; each Manager must own its own list")
	}

	// And a Manager really does construct its own.
	m1 := NewManager("th_a", fake.NewProvider(fake.Step{Text: "x"}), WithRoots(roots))
	m2 := NewManager("th_b", fake.NewProvider(fake.Step{Text: "x"}), WithRoots(roots))
	if m1.surface == m2.surface {
		t.Fatal("two Managers share one surface")
	}
}

// A rejected task.write must reach the model as a usable error result, not
// a hard failure that ends the turn.
func TestManager_TaskWrite_ValidationErrorIsAToolResult(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			taskWriteCall("1", []map[string]string{
				{"content": "a", "status": "in_progress"},
				{"content": "b", "status": "in_progress"},
			}),
		}},
		fake.Step{Text: "corrected"},
	)
	m := NewManager("th_taskerr", provider, WithRoots(roots),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 64)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "track badly"}
	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; a validation error should be a tool result the model can correct, "+
			"not a turn failure", got)
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if remaining := provider.StepsRemaining(); remaining != 0 {
		t.Fatalf("provider has %d steps left; the model never got a chance to correct", remaining)
	}
}
