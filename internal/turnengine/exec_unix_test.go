//go:build !windows

// These tests drive the exec family (run_command → /bin/sh) and use POSIX
// commands (echo), so they run on unix only. run_command is unix-oriented —
// Windows is a build/CI target for the package, not a runtime target for the
// exec family (mirrors internal/toolrunner/exec_test.go's //go:build !windows).
// The fs/dispatch/wire-format runner tests stay cross-platform in
// surfacerunner_test.go / manager_test.go.

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

// TestSurfaceRunner_RunCommandSuccess proves the exec family path (unix only).
func TestSurfaceRunner_RunCommandSuccess(t *testing.T) {
	roots := testRoots(t)
	surface := toolrunner.NewSurface(nil, toolrunner.NewFSFamily(roots), toolrunner.NewExecFamily(roots))
	runner := newSurfaceRunner(surface)

	raw, err := runner.Run(context.Background(), bridle.ToolCall{
		ID:   "1",
		Name: toolrunner.ToolRunCommand,
		Args: json.RawMessage(`{"command":"echo hi"}`),
	})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Run's return is not valid JSON: %v (raw=%s)", err, raw)
	}
	if strings.TrimSpace(got) != "hi" {
		t.Fatalf("got %q; want %q", got, "hi")
	}
}

// TestManager_ToolCall_RunCommandExecutesViaSurface: end-to-end through the
// bridle.Harness, a run_command tool call executes via the Surface (unix only).
func TestManager_ToolCall_RunCommandExecutesViaSurface(t *testing.T) {
	roots := managerTestRoots(t)

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolRunCommand, Args: json.RawMessage(`{"command":"echo hi"}`)},
		}},
		fake.Step{Text: "done"},
	)
	// U-C3: every tool call now goes through the approval gate — allow-all
	// so this test keeps proving DISPATCH (the exec family actually runs),
	// not approval semantics (which has its own dedicated coverage in
	// approval_test.go).
	m := NewManager("th_tool", provider, WithRoots(roots), WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "run echo hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	toolMsg := lastToolResultMessage(t, provider.LastRequest())
	if !strings.Contains(toolMsg.Content, "hi") {
		t.Fatalf("tool_result content = %q; want it to contain the command's real stdout", toolMsg.Content)
	}
}
