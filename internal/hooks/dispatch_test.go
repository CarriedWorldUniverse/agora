package hooks

import (
	"context"
	"testing"
	"time"
)

// fakeClock is an injectable Clock whose After never fires unless the test
// explicitly sends on the channel it returns — no wall-clock sleeps
// anywhere (ground rule 4). A single fakeClock only supports one
// outstanding After() call per test in this file (each test creates its
// own), which is all these tests need.
type fakeClock struct {
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{ch: make(chan time.Time)} }

func (f *fakeClock) After(time.Duration) <-chan time.Time { return f.ch }

func (f *fakeClock) fire() { f.ch <- time.Time{} }

func runnableHandler(cmd string, async bool) ResolvedHandler {
	return ResolvedHandler{
		RegisteredHandler: RegisteredHandler{
			Handler: Handler{Type: HandlerCommand, Command: cmd, Async: async, Timeout: DefaultTimeoutSeconds},
		},
		Runnable: true,
	}
}

// TestDispatch_AsyncDoesNotBlockTheTurn is the DoD's core async proof:
// Dispatch must return while an async handler's RunFunc is still pending —
// proven with a gate CHANNEL the async RunFunc blocks on, controlled
// entirely by the test, never a real sleep.
func TestDispatch_AsyncDoesNotBlockTheTurn(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 1)

	run := func(ctx context.Context, rh ResolvedHandler, event EventName, stdin []byte) RunResult {
		if rh.Handler.Async {
			started <- struct{}{}
			<-gate // blocks until the test releases it — proves Dispatch didn't wait.
			return RunResult{ExitCode: 0}
		}
		return RunResult{ExitCode: 0}
	}

	asyncResults := make(chan AsyncResult, 1)
	d := &Dispatcher{Run: run, AsyncResults: asyncResults}

	matched := []ResolvedHandler{runnableHandler("slow-async-hook", true)}

	done := make(chan []SyncResult, 1)
	go func() {
		done <- d.Dispatch(context.Background(), EventStop, matched, []byte("{}"))
	}()

	// Wait for the async handler to actually have started (so we know it's
	// genuinely blocked on the gate, not just "hasn't run yet"), THEN
	// confirm Dispatch has already returned — proving non-blocking.
	<-started

	select {
	case <-done:
		// Dispatch returned even though the async handler is still parked
		// on gate — exactly the property under test.
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch blocked on an async handler — async must be fire-and-forget")
	}

	// Now release the async handler and confirm its result eventually
	// arrives on AsyncResults, without leaking the goroutine.
	close(gate)
	select {
	case res := <-asyncResults:
		if res.Result.ExitCode != 0 {
			t.Errorf("unexpected async result: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async result never arrived on AsyncResults")
	}
	d.Wait() // no goroutine leak: this must return promptly.
}

// TestDispatch_SyncHandlerIsAwaited proves the converse: a sync handler's
// result IS in Dispatch's return value (no gate needed — sync work isn't
// racy against Dispatch's return by construction, only async is).
func TestDispatch_SyncHandlerIsAwaited(t *testing.T) {
	run := func(ctx context.Context, rh ResolvedHandler, event EventName, stdin []byte) RunResult {
		return RunResult{ExitCode: 0, Stdout: []byte("ok")}
	}
	d := &Dispatcher{Run: run}
	matched := []ResolvedHandler{runnableHandler("fast-sync-hook", false)}
	results := d.Dispatch(context.Background(), EventStop, matched, []byte("{}"))
	if len(results) != 1 || results[0].Skipped {
		t.Fatalf("expected one non-skipped sync result, got %+v", results)
	}
	if string(results[0].Result.Stdout) != "ok" {
		t.Errorf("Stdout = %q, want ok", results[0].Result.Stdout)
	}
	if results[0].CompletionIndex != 0 {
		t.Errorf("CompletionIndex = %d, want 0 (only handler)", results[0].CompletionIndex)
	}
}

// TestDispatch_NonRunnableHandlerNeverInvokesRun is the security-relevant
// fail-closed proof at the dispatch layer: an untrusted/disabled handler
// must never reach RunFunc at all (ground rule 6).
func TestDispatch_NonRunnableHandlerNeverInvokesRun(t *testing.T) {
	called := false
	run := func(ctx context.Context, rh ResolvedHandler, event EventName, stdin []byte) RunResult {
		called = true
		return RunResult{ExitCode: 0}
	}
	d := &Dispatcher{Run: run}
	matched := []ResolvedHandler{{
		RegisteredHandler: RegisteredHandler{Handler: Handler{Type: HandlerCommand, Command: "untrusted", Timeout: DefaultTimeoutSeconds}},
		TrustState:        TrustUntrusted,
		Runnable:          false,
	}}
	results := d.Dispatch(context.Background(), EventStop, matched, []byte("{}"))
	if called {
		t.Fatal("RunFunc must never be invoked for a non-runnable (untrusted) handler")
	}
	if len(results) != 1 || !results[0].Skipped {
		t.Fatalf("expected one Skipped result, got %+v", results)
	}
}

// TestDispatch_Timeout uses the injected fakeClock (not a real sleep) to
// prove a handler that never returns is reported TimedOut once the clock
// fires.
func TestDispatch_Timeout(t *testing.T) {
	fc := newFakeClock()
	neverReturns := make(chan struct{})
	run := func(ctx context.Context, rh ResolvedHandler, event EventName, stdin []byte) RunResult {
		<-neverReturns // would block forever without the timeout race
		return RunResult{ExitCode: 0}
	}
	d := &Dispatcher{Run: run, Clock: fc}
	matched := []ResolvedHandler{runnableHandler("hangs-forever", false)}

	done := make(chan []SyncResult, 1)
	go func() {
		done <- d.Dispatch(context.Background(), EventStop, matched, []byte("{}"))
	}()

	fc.fire() // simulate the timeout elapsing, no wall-clock wait.

	select {
	case results := <-done:
		if len(results) != 1 || !results[0].TimedOut {
			t.Fatalf("expected TimedOut result, got %+v", results)
		}
		if results[0].Result.ExitCode != -1 {
			t.Errorf("timed-out handler should report a sentinel exit code, got %d", results[0].Result.ExitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch did not return after the fake clock fired")
	}
	close(neverReturns)
}
