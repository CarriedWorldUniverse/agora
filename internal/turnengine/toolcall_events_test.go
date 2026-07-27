// Cross-platform tool-call item event tests (U-C5, NEX-784). run_command's
// own end-to-end coverage lives in toolcall_events_unix_test.go (it shells
// out via /bin/sh — see this repo's standing "//go:build !windows" rule for
// any test that does); everything here drives write_file/read_file/
// mcp__-prefixed tools, none of which touch a shell.

package turnengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// TestManager_ToolCall_WriteFile_ItemEvents: a write_file call emits
// item.started/item.completed{file_change} carrying the path, ordered
// after turn.started and before turn.completed, with Start and Result
// sharing one seq.
func TestManager_ToolCall_WriteFile_ItemEvents(t *testing.T) {
	roots := managerTestRoots(t)
	args, err := json.Marshal(map[string]string{"path": "out.txt", "content": "hello"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolWriteFile, Args: args},
		}},
		fake.Step{Text: "done"},
	)
	// U-C3 gates every tool call; allow-all so this test proves item-event
	// translation, not approval semantics (its own coverage in approval_test.go).
	m := NewManager("th_wf", provider, WithRoots(roots), WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write out.txt"}

	started := recvWithin(t, out, testTimeout)
	if started.Type != contracts.EvTurnStarted {
		t.Fatalf("first event = %+v; want turn.started", started)
	}

	itemStarted := recvWithin(t, out, testTimeout)
	if itemStarted.Type != contracts.EvItemStarted {
		t.Fatalf("second event = %+v; want item.started", itemStarted)
	}
	if itemStarted.Item == nil || itemStarted.Item.Type != contracts.ItemFileChange {
		t.Fatalf("item.started Item = %+v; want type file_change", itemStarted.Item)
	}
	var startedPayload fileChangeStartedPayload
	if err := json.Unmarshal(itemStarted.Payload, &startedPayload); err != nil {
		t.Fatalf("decode item.started payload: %v", err)
	}
	if startedPayload.Path != "out.txt" {
		t.Fatalf("item.started path = %q; want out.txt", startedPayload.Path)
	}

	itemCompleted := recvWithin(t, out, testTimeout)
	if itemCompleted.Type != contracts.EvItemCompleted {
		t.Fatalf("third event = %+v; want item.completed", itemCompleted)
	}
	if itemCompleted.Item == nil || itemCompleted.Item.Type != contracts.ItemFileChange {
		t.Fatalf("item.completed Item = %+v; want type file_change", itemCompleted.Item)
	}
	if itemCompleted.Item.Seq != itemStarted.Item.Seq {
		t.Fatalf("item.completed seq = %d; want the SAME seq as item.started (%d) — Start/Result must correlate by call ID", itemCompleted.Item.Seq, itemStarted.Item.Seq)
	}
	var completedPayload fileChangeCompletedPayload
	if err := json.Unmarshal(itemCompleted.Payload, &completedPayload); err != nil {
		t.Fatalf("decode item.completed payload: %v", err)
	}
	if completedPayload.Path != "out.txt" {
		t.Fatalf("item.completed path = %q; want out.txt", completedPayload.Path)
	}
	if completedPayload.Error != "" {
		t.Fatalf("item.completed error = %q; want empty (write should succeed)", completedPayload.Error)
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

// TestManager_ToolCall_MCPTool_ItemEvents: an mcp__-prefixed tool call
// emits item.started/item.completed{mcp_tool_call} carrying the tool
// name and raw args/result, correlated by the same seq.
func TestManager_ToolCall_MCPTool_ItemEvents(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: "mcp__herald__send_message", Args: json.RawMessage(`{"channel":"general"}`)},
		}},
		fake.Step{Text: "done"},
	)
	// No native family handles mcp__ tools (U-C2's Surface is fs/exec only,
	// nil MCPSource — see manager.go's NewManager doc comment); the call
	// dispatch-errors, but ToolCallStart/Result still fire from bridle
	// unconditionally (run.go's executeToolCall) — exactly what this test
	// is proving item-event translation against, independent of dispatch
	// outcome.
	m := NewManager("th_mcp", provider, WithRoots(roots), WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "send a message"}

	recvWithin(t, out, testTimeout) // turn.started

	itemStarted := recvWithin(t, out, testTimeout)
	if itemStarted.Type != contracts.EvItemStarted {
		t.Fatalf("event = %+v; want item.started", itemStarted)
	}
	if itemStarted.Item == nil || itemStarted.Item.Type != contracts.ItemMCPToolCall {
		t.Fatalf("item.started Item = %+v; want type mcp_tool_call", itemStarted.Item)
	}
	var startedPayload mcpToolCallStartedPayload
	if err := json.Unmarshal(itemStarted.Payload, &startedPayload); err != nil {
		t.Fatalf("decode item.started payload: %v", err)
	}
	if startedPayload.Tool != "mcp__herald__send_message" {
		t.Fatalf("item.started tool = %q; want mcp__herald__send_message", startedPayload.Tool)
	}
	if string(startedPayload.Args) != `{"channel":"general"}` {
		t.Fatalf("item.started args = %s; want the raw call args untouched", startedPayload.Args)
	}

	itemCompleted := recvWithin(t, out, testTimeout)
	if itemCompleted.Type != contracts.EvItemCompleted {
		t.Fatalf("event = %+v; want item.completed", itemCompleted)
	}
	if itemCompleted.Item.Seq != itemStarted.Item.Seq || itemCompleted.Item.Type != contracts.ItemMCPToolCall {
		t.Fatalf("item.completed Item = %+v; want same seq (%d) and type mcp_tool_call", itemCompleted.Item, itemStarted.Item.Seq)
	}
	var completedPayload mcpToolCallCompletedPayload
	if err := json.Unmarshal(itemCompleted.Payload, &completedPayload); err != nil {
		t.Fatalf("decode item.completed payload: %v", err)
	}
	if completedPayload.Tool != "mcp__herald__send_message" {
		t.Fatalf("item.completed tool = %q; want mcp__herald__send_message", completedPayload.Tool)
	}
	if completedPayload.Error == "" {
		t.Fatalf("item.completed error = %q; want a non-empty dispatch error (no MCPSource is wired)", completedPayload.Error)
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

// TestManager_ToolCall_ReadFile_CommandExecutionSummary: read_file (a
// read-only fs tool with no dedicated ItemType) falls back to
// ItemCommandExecution with a synthesized "read_file <path>" command
// summary, both at item.started and item.completed.
func TestManager_ToolCall_ReadFile_CommandExecutionSummary(t *testing.T) {
	roots := managerTestRoots(t)
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, "hello.txt"), []byte("hello from disk"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolReadFile, Args: json.RawMessage(`{"path":"hello.txt"}`)},
		}},
		fake.Step{Text: "done"},
	)
	// read_file classifies as KindRead (NEX-782), which defaultPolicy()
	// already auto-allows — no WithPolicy override needed (matches
	// manager_test.go's TestManager_ToolCall_ReadFileExecutesViaSurface).
	m := NewManager("th_rf", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "read hello.txt"}

	recvWithin(t, out, testTimeout) // turn.started

	itemStarted := recvWithin(t, out, testTimeout)
	if itemStarted.Item == nil || itemStarted.Item.Type != contracts.ItemCommandExecution {
		t.Fatalf("item.started Item = %+v; want type command_execution (reads fall back to it)", itemStarted.Item)
	}
	var startedPayload commandExecStartedPayload
	if err := json.Unmarshal(itemStarted.Payload, &startedPayload); err != nil {
		t.Fatalf("decode item.started payload: %v", err)
	}
	if startedPayload.Command != "Read hello.txt" {
		t.Fatalf("item.started command = %q; want %q", startedPayload.Command, "Read hello.txt")
	}

	itemCompleted := recvWithin(t, out, testTimeout)
	if itemCompleted.Item.Seq != itemStarted.Item.Seq || itemCompleted.Item.Type != contracts.ItemCommandExecution {
		t.Fatalf("item.completed Item = %+v; want same seq (%d) and type command_execution", itemCompleted.Item, itemStarted.Item.Seq)
	}
	var completedPayload commandExecCompletedPayload
	if err := json.Unmarshal(itemCompleted.Payload, &completedPayload); err != nil {
		t.Fatalf("decode item.completed payload: %v", err)
	}
	if completedPayload.Command != "Read hello.txt" {
		t.Fatalf("item.completed command = %q; want %q", completedPayload.Command, "Read hello.txt")
	}
	if completedPayload.Output != "hello from disk" {
		t.Fatalf("item.completed output = %q; want the file's real content", completedPayload.Output)
	}
	if completedPayload.Error != "" {
		t.Fatalf("item.completed error = %q; want empty", completedPayload.Error)
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

// TestManager_ToolCall_MalformedArgs_NoPanic: a write_file call whose Args
// is not valid JSON must not panic the sink — toolArgPath best-effort
// decodes it, falls back to an empty path, and the turn still completes
// normally. (Classify itself routes malformed write_file args to
// KindEscalation — allowAllPolicy auto-allows it too — so this exercises
// the sink's OWN decode robustness, not the approval gate's.)
func TestManager_ToolCall_MalformedArgs_NoPanic(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolWriteFile, Args: json.RawMessage(`not valid json`)},
		}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_bad_args", provider, WithRoots(roots), WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Run panicked on malformed tool-call Args: %v", r)
			}
		}()
		go func() { runErr <- m.Run(context.Background(), in, out) }()

		in <- contracts.Input{Type: contracts.InUserMessage, Text: "write something"}

		recvWithin(t, out, testTimeout) // turn.started

		itemStarted := recvWithin(t, out, testTimeout)
		if itemStarted.Item == nil || itemStarted.Item.Type != contracts.ItemFileChange {
			t.Fatalf("item.started Item = %+v; want type file_change", itemStarted.Item)
		}
		var startedPayload fileChangeStartedPayload
		if err := json.Unmarshal(itemStarted.Payload, &startedPayload); err != nil {
			t.Fatalf("decode item.started payload: %v", err)
		}
		if startedPayload.Path != "" {
			t.Fatalf("item.started path = %q; want empty (Args did not decode)", startedPayload.Path)
		}

		if !drainToTurnCompleted(t, out, testTimeout) {
			t.Fatal("turn never completed")
		}
	}()

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}
