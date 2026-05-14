// Package engine drives agora's deliberation loop.
//
// NEX-82: agora consumes funnel.Funnel as the deliberation engine.
// All compaction, session resolution, filter judgment, and
// observability live in funnel; agora plugs in via:
//
//   - AgoraReturnHandler — funnel.ReturnHandler that routes results
//     by trigger.Source (chat → bus.SendChat; tty → panel only).
//   - UIHook — funnel.ObservabilityHook that pipes bridle ModelChunk
//     events into the TUI live-line render.
//   - notify_operator post-hoc parse (NEX-63) — applied inside the
//     ReturnHandler's Handle path before routing the cleaned reply.
//
// The engine itself is a thin loop that pushes items into funnel
// and wakes funnel.Deliberate when there's pending work.
package engine

import (
	"context"
	"errors"
	"log/slog"

	bridle "github.com/CarriedWorldUniverse/bridle"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"
)

// Config bundles the engine's dependencies. All fields are required.
type Config struct {
	Funnel *funnel.Funnel
	Logger *slog.Logger
}

// Engine wraps a funnel.Funnel with a wake-up signal channel so
// agora can push inbox items at any time and have the deliberate
// loop drain them promptly.
type Engine struct {
	cfg  Config
	wake chan struct{}
}

// New constructs an Engine. The funnel must already be wired with
// its ReturnHandler + ObservabilityHook before construction.
func New(cfg Config) *Engine {
	return &Engine{
		cfg:  cfg,
		wake: make(chan struct{}, 1),
	}
}

// Receive pushes an inbox item into the funnel + wakes the deliberate
// loop. Convenience wrapper; equivalent to Funnel.Receive + Signal.
//
// Callers set item.Source ("chat" or "tty") to drive the
// ReturnHandler's routing decision. Empty Source defaults to "chat"
// downstream.
func (e *Engine) Receive(item bridle.InboxItem) {
	e.cfg.Funnel.Receive(item)
	e.signal()
}

// signal coalesces wake-ups: multiple Receive calls between
// deliberate-loop iterations produce one wake. Cap-1 buffered chan +
// non-blocking send.
func (e *Engine) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// InboxLen surfaces funnel's queue depth for the UI status line.
func (e *Engine) InboxLen() int {
	return e.cfg.Funnel.InboxLen()
}

// Run blocks until ctx is cancelled. Each wake drains the funnel's
// inbox (calling Deliberate until ErrEmptyInbox). One Deliberate
// pops exactly one item per #224; the funnel's compaction + session
// machinery runs per turn.
func (e *Engine) Run(ctx context.Context) {
	e.cfg.Logger.Info("engine running")
	for {
		select {
		case <-ctx.Done():
			e.cfg.Logger.Info("engine stopping", "err", ctx.Err())
			return
		case <-e.wake:
			e.drain(ctx)
		}
	}
}

// drain calls Deliberate in a loop until the funnel's inbox is empty
// or an error fires. Per-turn errors are logged but don't kill the
// loop — the ReturnHandler has already surfaced the error to the UI
// (or chat) by the time we see it here.
func (e *Engine) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, err := e.cfg.Funnel.Deliberate(ctx, "")
		if errors.Is(err, funnel.ErrEmptyInbox) {
			return
		}
		if err != nil {
			e.cfg.Logger.Error("deliberate failed", "err", err)
			// Don't return — try the next item if there is one. The
			// loop terminates naturally on ErrEmptyInbox.
		}
	}
}
