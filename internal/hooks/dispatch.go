package hooks

import (
	"context"
	"sync"
	"time"
)

// Clock abstracts time so the engine's timeout handling is testable without
// wall-clock sleeps (ground rule 4). RealClock is the production
// implementation; tests inject a fake that is driven by an explicit
// channel/tick rather than real elapsed time.
type Clock interface {
	After(d time.Duration) <-chan time.Time
}

// RealClock is the production Clock, backed by time.After.
type RealClock struct{}

func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RunResult is what a RunFunc reports for one handler invocation.
type RunResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Err      error // set for an invocation-level failure distinct from exit code (e.g. spawn error).
}

// RunFunc actually executes a handler's command and returns its result.
// This package never implements RunFunc itself — spawning `$SHELL -lc
// <command>` / `%COMSPEC% /C` (§3 "Invocation") is the daemon's job; tests
// in this package supply a stub (ground rule 6: model the engine, don't
// spawn real processes).
type RunFunc func(ctx context.Context, rh ResolvedHandler, event EventName, stdin []byte) RunResult

// AsyncResult is what an async handler reports once it completes, deferred
// off the SyncResult path so the turn is never blocked on it (§1.4 build
// note: "Implement async: true properly... Go makes this trivial: goroutine
// + no result wait").
type AsyncResult struct {
	Handler  ResolvedHandler
	Event    EventName
	Result   RunResult
	TimedOut bool
}

// SyncResult is one synchronous handler's outcome from a Dispatch call,
// carrying enough for aggregate.go's Outcome (CompletionIndex is assigned
// by Dispatch in the order sync handlers actually finish).
type SyncResult struct {
	Handler         ResolvedHandler
	CompletionIndex int
	// Skipped is true when the handler was not runnable (trust gate
	// failed — TrustUntrusted/TrustModified/disabled) and Run was never
	// called (ground rule 6: fail closed, no silent execution).
	Skipped  bool
	Result   RunResult
	TimedOut bool
}

// Dispatcher runs matched+resolved handlers for one event firing, splitting
// sync (awaited) from async (fire-and-forget) per Handler.Async (§1.4).
type Dispatcher struct {
	Run   RunFunc
	Clock Clock
	// AsyncResults, if non-nil, receives every async handler's outcome once
	// it completes. Dispatch never blocks sending here beyond whatever
	// buffering the caller gave the channel — a full unbuffered channel
	// with no reader would block the completing goroutine, not the turn
	// (Dispatch itself has already returned by then); tests use a buffered
	// channel sized to the number of async handlers so nothing is dropped.
	AsyncResults chan<- AsyncResult

	wg sync.WaitGroup
}

// Dispatch runs every runnable sync handler in matched to completion,
// concurrently (§3: "Run concurrently, results re-sorted to configured
// order for reporting; completion order is tracked only to pick
// PreToolUse's last-finished updatedInput"), and fires every runnable async
// handler without waiting. Non-runnable (trust-gated) handlers are reported
// Skipped and never passed to Run. Dispatch returns as soon as all SYNC
// work is done; async completions arrive later on AsyncResults (or are
// silently dropped if AsyncResults is nil and the caller doesn't want
// them — that's a valid, if lossy, configuration for a fire-and-forget
// hook the daemon doesn't care to audit).
func (d *Dispatcher) Dispatch(ctx context.Context, event EventName, matched []ResolvedHandler, stdin []byte) []SyncResult {
	clock := d.Clock
	if clock == nil {
		clock = RealClock{}
	}

	results := make([]SyncResult, len(matched))
	var syncMu sync.Mutex
	var syncWG sync.WaitGroup
	completion := 0

	for i, rh := range matched {
		if !rh.Runnable {
			results[i] = SyncResult{Handler: rh, Skipped: true, CompletionIndex: -1}
			continue
		}
		if rh.Handler.Async {
			d.runAsync(ctx, rh, event, stdin, clock)
			results[i] = SyncResult{Handler: rh, Skipped: true, CompletionIndex: -1} // not a sync result; caller reads AsyncResults for outcome
			continue
		}
		syncWG.Add(1)
		go func(i int, rh ResolvedHandler) {
			defer syncWG.Done()
			res, timedOut := d.runWithTimeout(ctx, rh, event, stdin, clock)
			syncMu.Lock()
			idx := completion
			completion++
			results[i] = SyncResult{Handler: rh, CompletionIndex: idx, Result: res, TimedOut: timedOut}
			syncMu.Unlock()
		}(i, rh)
	}
	syncWG.Wait()
	return results
}

// Wait blocks until every async handler fired by any Dispatch call on this
// Dispatcher has completed. Production code never needs this (async is
// fire-and-forget by design); it exists for test/shutdown cleanup so a
// test process doesn't leak goroutines (ground rule 4) or race the
// AsyncResults channel against test-end.
func (d *Dispatcher) Wait() { d.wg.Wait() }

func (d *Dispatcher) runAsync(ctx context.Context, rh ResolvedHandler, event EventName, stdin []byte, clock Clock) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		res, timedOut := d.runWithTimeout(ctx, rh, event, stdin, clock)
		if d.AsyncResults != nil {
			d.AsyncResults <- AsyncResult{Handler: rh, Event: event, Result: res, TimedOut: timedOut}
		}
	}()
}

// runWithTimeout races Run's completion against clock.After(timeout) (§3:
// "on timeout kill child, run = Failed"). Using the injected Clock rather
// than context.WithTimeout keeps timeout behavior testable without a real
// wall-clock wait (ground rule 4).
func (d *Dispatcher) runWithTimeout(ctx context.Context, rh ResolvedHandler, event EventName, stdin []byte, clock Clock) (RunResult, bool) {
	timeout := time.Duration(rh.Handler.Timeout) * time.Second
	done := make(chan RunResult, 1)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		done <- d.Run(runCtx, rh, event, stdin)
	}()
	select {
	case res := <-done:
		return res, false
	case <-clock.After(timeout):
		cancel() // §3: "Child killed on drop" — cancel signals the RunFunc to kill its child.
		return RunResult{ExitCode: -1, Err: context.DeadlineExceeded}, true
	}
}
