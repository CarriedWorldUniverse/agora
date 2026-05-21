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
	"sync"
	"time"

	bridle "github.com/CarriedWorldUniverse/bridle"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"
)

// ttyDedupeWindow is how long an identical SourceTTY submission must
// wait before it's accepted again. NEX-250: agora was observed delivering
// the same panel-route content as multiple consecutive turns; this drop
// gate keeps the funnel from acting on duplicate keystrokes regardless
// of upstream cause (history re-submission, key-repeat, UI confusion).
// Five seconds is long enough to catch any visible double-press / quick
// up-arrow re-submit, short enough that an operator who truly wants to
// re-send the same text just waits a beat.
const ttyDedupeWindow = 5 * time.Second

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

	// ttyMu guards lastTTY* — Receive may be called from the UI
	// goroutine (onSubmit) while drain runs the engine's goroutine.
	ttyMu          sync.Mutex
	lastTTYContent string
	lastTTYAt      time.Time
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
//
// NEX-250: SourceTTY submissions are deduplicated against the previous
// SourceTTY content within ttyDedupeWindow. Identical content arriving
// inside the window is dropped (logged at Debug) so the funnel doesn't
// see the same operator input as multiple consecutive turns. Chat-route
// items are passed through unchanged — peers re-asserting the same text
// usually signals genuine retry / clarification and shouldn't be eaten.
func (e *Engine) Receive(item bridle.InboxItem) {
	// NEX-250 investigation trace: log every Receive with source +
	// msgID + content excerpt so we can see whether a single operator
	// input arrives via more than one path (e.g. TTY direct AND a
	// broker echo). Excerpt clipped to keep activity log readable.
	if e.cfg.Logger != nil {
		excerpt := item.Content
		if len(excerpt) > 80 {
			excerpt = excerpt[:80] + "…"
		}
		e.cfg.Logger.Info("engine.Receive",
			"source", item.Source,
			"msg_id", item.MsgID,
			"from", item.From,
			"thread_root", item.ThreadRoot,
			"excerpt", excerpt)
	}
	if item.Source == SourceTTY {
		e.ttyMu.Lock()
		now := time.Now()
		if item.Content == e.lastTTYContent && now.Sub(e.lastTTYAt) < ttyDedupeWindow {
			e.ttyMu.Unlock()
			if e.cfg.Logger != nil {
				e.cfg.Logger.Info("engine: dropping duplicate tty submission",
					"window", ttyDedupeWindow,
					"since_last", now.Sub(e.lastTTYAt))
			}
			return
		}
		e.lastTTYContent = item.Content
		e.lastTTYAt = now
		e.ttyMu.Unlock()
	}
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
