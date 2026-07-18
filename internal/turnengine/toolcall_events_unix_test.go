//go:build !windows

// run_command shells out via /bin/sh (see exec_unix_test.go's own doc
// comment) — Windows lacks /bin/sh, so this test stays unix-only per this
// repo's standing rule. The rest of U-C5's tool-call item event coverage
// (write_file/read_file/mcp__) is cross-platform and lives in
// toolcall_events_test.go.

package turnengine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// TestManager_ToolCall_RunCommand_ItemEvents: a run_command call emits
// item.started{command_execution} then item.completed{command_execution}
// with the SAME seq, real stdout in Output, ordered strictly after
// turn.started and before turn.completed.
func TestManager_ToolCall_RunCommand_ItemEvents(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolRunCommand, Args: json.RawMessage(`{"command":"echo hi"}`)},
		}},
		fake.Step{Text: "done"},
	)
	// U-C3 gates every tool call under KindExec (defaultPolicy() prompts);
	// allow-all so this test proves item-event translation, not approval
	// semantics (matches exec_unix_test.go's own convention).
	m := NewManager("th_rc", provider, WithRoots(roots), WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "run echo hi"}

	started := recvWithin(t, out, testTimeout)
	if started.Type != contracts.EvTurnStarted {
		t.Fatalf("first event = %+v; want turn.started", started)
	}

	itemStarted := recvWithin(t, out, testTimeout)
	if itemStarted.Type != contracts.EvItemStarted {
		t.Fatalf("second event = %+v; want item.started", itemStarted)
	}
	if itemStarted.Item == nil || itemStarted.Item.Type != contracts.ItemCommandExecution {
		t.Fatalf("item.started Item = %+v; want type command_execution", itemStarted.Item)
	}
	var startedPayload commandExecStartedPayload
	if err := json.Unmarshal(itemStarted.Payload, &startedPayload); err != nil {
		t.Fatalf("decode item.started payload: %v", err)
	}
	if startedPayload.Command != "echo hi" {
		t.Fatalf("item.started command = %q; want %q (the real shell command)", startedPayload.Command, "echo hi")
	}

	itemCompleted := recvWithin(t, out, testTimeout)
	if itemCompleted.Type != contracts.EvItemCompleted {
		t.Fatalf("third event = %+v; want item.completed", itemCompleted)
	}
	if itemCompleted.Item == nil || itemCompleted.Item.Type != contracts.ItemCommandExecution {
		t.Fatalf("item.completed Item = %+v; want type command_execution", itemCompleted.Item)
	}
	if itemCompleted.Item.Seq != itemStarted.Item.Seq {
		t.Fatalf("item.completed seq = %d; want the SAME seq as item.started (%d)", itemCompleted.Item.Seq, itemStarted.Item.Seq)
	}
	var completedPayload commandExecCompletedPayload
	if err := json.Unmarshal(itemCompleted.Payload, &completedPayload); err != nil {
		t.Fatalf("decode item.completed payload: %v", err)
	}
	if completedPayload.Command != "echo hi" {
		t.Fatalf("item.completed command = %q; want %q", completedPayload.Command, "echo hi")
	}
	if !strings.Contains(completedPayload.Output, "hi") {
		t.Fatalf("item.completed output = %q; want it to contain the command's real stdout", completedPayload.Output)
	}
	if completedPayload.Error != "" {
		t.Fatalf("item.completed error = %q; want empty (echo succeeds)", completedPayload.Error)
	}

	// The tool item events landed strictly before turn.completed; drain the
	// rest of the turn (the second fake.Step's own agent_message item, then
	// turn.completed) rather than asserting turn.completed is the literal
	// next event.
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}

// TestManager_ToolCall_RunCommand_ErrorSurfacesInCompleted: a failing
// run_command still gets item.started/item.completed{command_execution},
// with the non-empty Err carried in the completed payload's error field.
func TestManager_ToolCall_RunCommand_ErrorSurfacesInCompleted(t *testing.T) {
	roots := managerTestRoots(t)
	// A bare "exit 3" produces empty stdout/stderr, which surfaceRunner's
	// errors.New(result.Content) would turn into an EMPTY error string —
	// echo to stderr first so the failure carries a real, non-empty
	// message to assert on.
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolRunCommand, Args: json.RawMessage(`{"command":"echo boom >&2; exit 3"}`)},
		}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_rc_err", provider, WithRoots(roots), WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "run a failing command"}

	recvWithin(t, out, testTimeout) // turn.started
	recvWithin(t, out, testTimeout) // item.started

	itemCompleted := recvWithin(t, out, testTimeout)
	var completedPayload commandExecCompletedPayload
	if err := json.Unmarshal(itemCompleted.Payload, &completedPayload); err != nil {
		t.Fatalf("decode item.completed payload: %v", err)
	}
	if completedPayload.Error == "" {
		t.Fatalf("item.completed error = %q; want a non-empty error (exit 3 fails)", completedPayload.Error)
	}

	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}
