//go:build !windows

// run_background shells out to /bin/sh, and this test's liveness check
// uses pgrep — both unix-only.

package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// pgrepAlive reports whether any process's full command line contains
// marker — a real OS process-table check, not an assertion about Go-level
// state. Deliberately does NOT reach into the Manager's unexported
// m.surface to find the concrete *toolrunner.ExecFamily (a cross-package
// field, inaccessible and rightly so): the same real-subprocess-liveness
// technique already proven for the MCP subprocess teardown fix earlier
// this session, applied here instead of trusting internal bookkeeping.
func pgrepAlive(t *testing.T, marker string) bool {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", marker).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false // pgrep's own "no match" exit code — not a real error
		}
		t.Fatalf("pgrep: %v", err)
	}
	return len(out) > 0
}

// TestManager_BackgroundJob_KilledWhenRunEnds is the Manager-level proof
// that a background job started via a REAL turn is reachable, and that
// Manager.Run's teardown actually kills it — not a unit test of
// ExecFamily.Close in isolation, but the same shape of wiring gap #100
// (agent defs never loaded) and the MCP subprocess leak both were: a
// mechanism that works perfectly on its own but is never actually reached
// from production construction.
func TestManager_BackgroundJob_KilledWhenRunEnds(t *testing.T) {
	roots := managerTestRoots(t)
	// A marker unique to this test run, embedded in the command line so
	// pgrep can find exactly this process and nothing else — parallel
	// test runs or a stray unrelated "sleep" must not produce a false
	// positive/negative.
	marker := fmt.Sprintf("agora-bgwire-test-%d", time.Now().UnixNano())
	// Two statements, not one — this structurally prevents the shell from
	// tail-call-exec-replacing itself with `sleep` (which some shells do
	// for a single simple command in -c position, and would silently drop
	// the marker from the visible process table): with a second statement
	// pending, /bin/sh -c MUST stay alive to run it, so its own argv (the
	// full string, marker included) is what a real ps/pgrep observes.
	command := fmt.Sprintf(": %s; sleep 30", marker)

	args, _ := json.Marshal(map[string]string{"command": command})
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{{ID: "1", Name: "run_background", Args: args}}},
		fake.Step{Text: "started it"},
	)
	m := NewManager("th_bg", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 64)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "start a background job"}
	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; want turn.completed (an in-sandbox run_background auto-runs "+
			"under sandbox-auto, same as run_command)", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !pgrepAlive(t, marker) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !pgrepAlive(t, marker) {
		t.Fatal("background job process never appeared in the process table — run_background did not actually start it")
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for pgrepAlive(t, marker) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pgrepAlive(t, marker) {
		t.Fatal("background job process still alive after Manager.Run returned — it outlived the session")
	}
}
