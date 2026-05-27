package main

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// recoverGoroutine is a defer-helper for every spawned goroutine.
//
// Operator-reported 2026-05-27: agora screen got "a mess of error
// messages and mouse movement hex" while still responding to /exit.
// Root cause: an unprotected side-goroutine panicked, Go runtime
// dumped its stack to stderr, stderr shared the terminal with
// bubbletea's alt-screen, panic text scribbled over the rendered UI
// while bubbletea's main Update loop kept running. This helper bounds
// the blast radius: catch the panic, write a full stack to the log
// file (where it can actually be read later), optionally cancel the
// rootCtx so the program initiates graceful shutdown when a
// load-bearing goroutine dies.
//
// name identifies the goroutine in logs (e.g. "bus.Run", "engine.Run",
// "signal-handler"). log is the file logger; nil is tolerated but
// loses the stack capture. cancel may be nil for non-load-bearing
// goroutines (one-shot signal handlers, idempotent senders) where
// panic shouldn't tear down the whole program.
//
// Usage:
//
//	go func() {
//	    defer recoverGoroutine("bus.Run", log, cancel)
//	    busDone <- b.Run(rootCtx)
//	}()
func recoverGoroutine(name string, log *slog.Logger, cancel context.CancelFunc) {
	r := recover()
	if r == nil {
		return
	}
	stack := string(debug.Stack())
	if log != nil {
		log.Error("agora: goroutine panicked",
			"goroutine", name,
			"panic", fmt.Sprintf("%v", r),
			"stack", stack)
	}
	if cancel != nil {
		// Load-bearing goroutine death → initiate graceful shutdown.
		// The TUI will see ctx cancellation, fire its own exit path,
		// terminal restores cleanly.
		cancel()
	}
}
