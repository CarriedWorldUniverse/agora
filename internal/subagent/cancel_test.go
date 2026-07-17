package subagent

import (
	"context"
	"errors"
	"testing"
)

// buildCancelFixture spawns, under root, a fixed graph shape backed by a
// single shared blockingRunner (nothing completes on its own — cancellation
// is the only thing that resolves these nodes short of releasing r):
//
//	root -> a (edge Foreground=true)
//	root -> b (edge Foreground=false)
//	a    -> c (edge Foreground=false, a's own child — not root's direct child)
//
// This shape distinguishes the three §2a triggers: turn-interrupt only ever
// touches root's DIRECT foreground children (a); workflow-stop/teardown
// walk the whole open subtree (a, b, c) regardless of the Foreground bit.
// Spawn always returns immediately (see SpawnOpts.Foreground doc comment),
// so building this concurrently-running fixture needs no goroutines.
func buildCancelFixture(t *testing.T) (*Manager, *blockingRunner, map[string]string) {
	t.Helper()
	r := newBlockingRunner()
	m := newTestManager(t, r, WithDepthCap(2))

	a, err := m.Spawn(context.Background(), "root", "a", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("spawn a: %v", err)
	}
	b, err := m.Spawn(context.Background(), "root", "b", SpawnOpts{Foreground: false})
	if err != nil {
		t.Fatalf("spawn b: %v", err)
	}
	c, err := m.Spawn(context.Background(), a, "c", SpawnOpts{Foreground: false})
	if err != nil {
		t.Fatalf("spawn c: %v", err)
	}

	for _, id := range []string{a, b, c} {
		if st, ok := m.Status(id); !ok || st != NodeRunning {
			t.Fatalf("fixture setup: %s status = %v ok=%v, want running before any cancel", id, st, ok)
		}
	}

	return m, r, map[string]string{"a": a, "b": b, "c": c}
}

// TestCancel_Matrix is the table-driven transcription of
// agora-spec-subagents.md §2a required by the DoD.
func TestCancel_Matrix(t *testing.T) {
	cases := []struct {
		name           string
		trigger        Trigger
		wantCancelled  []string // fixture keys (a/b/c)
		wantStillAlive []string // fixture keys
	}{
		{
			name:           "turn interrupt cancels only root's direct foreground children",
			trigger:        TriggerTurnInterrupt,
			wantCancelled:  []string{"a"},
			wantStillAlive: []string{"b", "c"},
		},
		{
			name:          "workflow stop cancels the whole open subtree regardless of foreground/background",
			trigger:       TriggerWorkflowStop,
			wantCancelled: []string{"a", "b", "c"},
		},
		{
			name:          "thread teardown cancels the whole open subtree regardless of foreground/background",
			trigger:       TriggerThreadTeardown,
			wantCancelled: []string{"a", "b", "c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, r, ids := buildCancelFixture(t)
			defer close(r.release)

			result, err := m.CancelPropagate(tc.trigger, "root")
			if err != nil {
				t.Fatalf("CancelPropagate: %v", err)
			}

			gotCancelled := map[string]bool{}
			for _, cid := range result.Cancelled {
				for key, id := range ids {
					if id == cid {
						gotCancelled[key] = true
					}
				}
			}
			for _, key := range tc.wantCancelled {
				if !gotCancelled[key] {
					t.Errorf("%s: expected cancelled, was not (result.Cancelled=%v)", key, result.Cancelled)
				}
				// No orphaned running node: every cancelled id must show
				// NodeInterrupted, never left running.
				status, ok := m.Status(ids[key])
				if !ok || status != NodeInterrupted {
					t.Errorf("%s: Status = %v ok=%v, want interrupted (no orphaned running node)", key, status, ok)
				}
			}
			for _, key := range tc.wantStillAlive {
				if gotCancelled[key] {
					t.Errorf("%s: expected still running, was cancelled", key)
				}
				status, ok := m.Status(ids[key])
				if !ok || status != NodeRunning {
					t.Errorf("%s: Status = %v, want still running", key, status)
				}
			}
		})
	}
}

// TestCancel_TurnInterruptThenTeardown_NoOrphanedRunningNode is FIX 1's
// regression: a turn-interrupt closes root->a while a's own background
// child c is still running; a later thread-teardown on root must still
// reach and cancel c (a closed intermediate edge must not hide a
// still-running descendant from cancellation traversal — "no orphaned
// running node" is an invariant, not a best-effort).
func TestCancel_TurnInterruptThenTeardown_NoOrphanedRunningNode(t *testing.T) {
	m, r, ids := buildCancelFixture(t)
	defer close(r.release)

	// Turn-interrupt cancels a (root's direct foreground child) and closes
	// root->a; c (a's own background child) is left running, unreachable via
	// an openOnly BFS from root once root->a is closed.
	interruptResult, err := m.CancelPropagate(TriggerTurnInterrupt, "root")
	if err != nil {
		t.Fatalf("CancelPropagate(turn_interrupt): %v", err)
	}
	if len(interruptResult.Cancelled) != 1 || interruptResult.Cancelled[0] != ids["a"] {
		t.Fatalf("turn_interrupt Cancelled = %v, want [a]", interruptResult.Cancelled)
	}
	if st, _ := m.Status(ids["c"]); st != NodeRunning {
		t.Fatalf("c Status after turn_interrupt = %v, want still running", st)
	}

	// A later thread-teardown on root must still find and cancel c even
	// though root->a is now closed.
	teardownResult, err := m.CancelPropagate(TriggerThreadTeardown, "root")
	if err != nil {
		t.Fatalf("CancelPropagate(thread_teardown): %v", err)
	}
	found := false
	for _, id := range teardownResult.Cancelled {
		if id == ids["c"] {
			found = true
		}
	}
	if !found {
		t.Errorf("thread_teardown Cancelled = %v, want it to include c (%s)", teardownResult.Cancelled, ids["c"])
	}
	if st, ok := m.Status(ids["c"]); !ok || st != NodeInterrupted {
		t.Errorf("c Status after thread_teardown = %v ok=%v, want interrupted (orphaned running node)", st, ok)
	}
}

func TestCancel_NodeNotRunning_NoOp(t *testing.T) {
	m := newTestManager(t, &instantRunner{})
	id, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, _, ok := m.Result(id); !ok {
		t.Fatal("Result: agent not found")
	}
	cancelled, err := m.CancelNode(id)
	if err != nil {
		t.Fatalf("CancelNode: %v", err)
	}
	if cancelled {
		t.Error("CancelNode on an already-completed agent should be a no-op, not report cancelled")
	}
}

func TestCancel_UnknownNode(t *testing.T) {
	m := newTestManager(t, &instantRunner{})
	_, err := m.CancelNode("nope")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("err = %v, want ErrNodeNotFound", err)
	}
}

func TestCancel_UnknownTrigger(t *testing.T) {
	m := newTestManager(t, &instantRunner{})
	_, err := m.CancelPropagate(Trigger("bogus"), "root")
	if err == nil {
		t.Fatal("expected error for unknown trigger")
	}
}

func TestCancel_ResumableByContinuation(t *testing.T) {
	r := newBlockingRunner()
	m := newTestManager(t, r)
	id, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cancelled, err := m.CancelNode(id)
	if err != nil || !cancelled {
		t.Fatalf("CancelNode: cancelled=%v err=%v", cancelled, err)
	}
	status, _ := m.Status(id)
	if status != NodeInterrupted {
		t.Fatalf("Status = %v, want interrupted", status)
	}
	// Let the shared blockingRunner complete promptly for the continuation
	// attempt too (it would otherwise block forever waiting on r.release).
	close(r.release)
	// Spec §2a: "a cancelled child is resumable-by-continuation like any
	// finished agent."
	res, err := m.Continue(context.Background(), id, "pick up where you left off")
	if err != nil {
		t.Fatalf("Continue after cancel: %v", err)
	}
	if len(res.Output) == 0 {
		t.Error("Continue after cancel produced no output")
	}

	// The graph edge must have been reopened by Continue (spec §2a: still
	// part of the graph once resumed).
	e, ok, err := m.graph.Edge("root", id)
	if err != nil || !ok {
		t.Fatalf("graph.Edge: ok=%v err=%v", ok, err)
	}
	if e.Status != EdgeOpen {
		t.Errorf("edge Status after Continue = %v, want open (reopened)", e.Status)
	}
}
