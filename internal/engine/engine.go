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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	bridle "github.com/CarriedWorldUniverse/bridle"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"
)

// ttyDedupeWindow is how long an identical SourceTTY submission must
// wait before it's accepted again. NEX-250/NEX-252: agora is observed
// firing engine.Receive for identical TTY content multiple times,
// sometimes minutes apart, when the operator typed the message once
// (root cause unknown — bubbletea / terminal-driver / late-delivery
// suspected). Window must be long enough to absorb those late re-fires
// but short enough that a genuine "I want to resend this exact line"
// retry isn't eaten silently. 15 minutes is the empirical upper bound
// of NEX-252's observed re-fire spacing (9-minute gap seen 2026-05-22)
// with margin; an operator who really wants to resend the same text
// within that window can append a space or wait it out.
const ttyDedupeWindow = 15 * time.Minute

// ttyDedupeCap bounds the content-hash cache. Each entry costs ~64
// bytes; 64 entries handles realistic operator pacing while keeping
// the memory footprint trivial. Older entries evict by insertion order
// (FIFO) — a true LRU is overkill for this surface.
const ttyDedupeCap = 64

// Config bundles the engine's dependencies. All fields are required.
type Config struct {
	Funnel *funnel.Funnel
	Logger *slog.Logger

	// OnDrop, if set, is invoked when a TTY submission is dropped by
	// the 15-min content-hash dedupe. Lets the UI surface a visible
	// system block explaining the drop. Reason is a short tag for
	// future expansion ("tty-duplicate"); firstSeen is the timestamp
	// of the original submission of the identical content.
	OnDrop func(reason string, firstSeen time.Time)
}

// Engine wraps a funnel.Funnel with a wake-up signal channel so
// agora can push inbox items at any time and have the deliberate
// loop drain them promptly.
type Engine struct {
	cfg  Config
	wake chan struct{}

	// ttyMu guards ttyHashes — Receive may be called from the UI
	// goroutine (onSubmit) while drain runs the engine's goroutine.
	ttyMu     sync.Mutex
	ttyHashes map[string]time.Time // sha256(content) → first-seen timestamp
	ttyOrder  []string             // insertion order, for FIFO eviction at ttyDedupeCap
}

// New constructs an Engine. Validates required Config fields per the
// Config docstring; returns an error rather than NPE'ing later from
// inside Run/drain/Receive on a missing Funnel or Logger.
func New(cfg Config) (*Engine, error) {
	if cfg.Funnel == nil {
		return nil, errors.New("engine: Config.Funnel required")
	}
	if cfg.Logger == nil {
		return nil, errors.New("engine: Config.Logger required (Run/drain log unconditionally)")
	}
	return &Engine{
		cfg:       cfg,
		wake:      make(chan struct{}, 1),
		ttyHashes: make(map[string]time.Time, ttyDedupeCap),
	}, nil
}

// Receive pushes an inbox item into the funnel + wakes the deliberate
// loop. Convenience wrapper; equivalent to Funnel.Receive + Signal.
//
// Callers set item.Source ("chat" or "tty") to drive the
// ReturnHandler's routing decision. Empty Source defaults to "chat"
// downstream.
//
// NEX-250/NEX-252: SourceTTY submissions are deduplicated by content
// hash within ttyDedupeWindow. Identical content arriving inside the
// window is dropped (logged at Info with hash + age) so the funnel
// doesn't see the same operator input as multiple consecutive turns.
// Chat-route items are passed through unchanged — peers re-asserting
// the same text usually signals genuine retry / clarification and
// shouldn't be eaten.
func (e *Engine) Receive(item bridle.InboxItem) {
	contentHash := hashContent(item.Content)

	// NEX-250/NEX-252 investigation trace: log every Receive with
	// source + msgID + content hash + length so duplicates are
	// unambiguous in the activity log (vs the previous 80-char
	// excerpt which collapsed to the same prefix for every
	// panel-route message).
	if e.cfg.Logger != nil {
		e.cfg.Logger.Info("engine.Receive",
			"source", item.Source,
			"msg_id", item.MsgID,
			"from", item.From,
			"thread_root", item.ThreadRoot,
			"content_sha", contentHash[:12],
			"content_len", len(item.Content))
	}
	if item.Source == SourceTTY {
		e.ttyMu.Lock()
		now := time.Now()
		if firstSeen, hit := e.ttyHashes[contentHash]; hit && now.Sub(firstSeen) < ttyDedupeWindow {
			e.ttyMu.Unlock()
			if e.cfg.Logger != nil {
				e.cfg.Logger.Info("engine: dropping duplicate tty submission",
					"window", ttyDedupeWindow,
					"since_first", now.Sub(firstSeen),
					"content_sha", contentHash[:12])
			}
			if e.cfg.OnDrop != nil {
				e.cfg.OnDrop("tty-duplicate", firstSeen)
			}
			return
		}
		// Record this hash. Bounded FIFO — at cap, evict the oldest
		// entry. This is a tiny cache; the bound is defensive, not a
		// performance concern.
		if _, present := e.ttyHashes[contentHash]; !present {
			if len(e.ttyOrder) >= ttyDedupeCap {
				oldest := e.ttyOrder[0]
				e.ttyOrder = e.ttyOrder[1:]
				delete(e.ttyHashes, oldest)
			}
			e.ttyOrder = append(e.ttyOrder, contentHash)
		}
		e.ttyHashes[contentHash] = now
		e.ttyMu.Unlock()
	}
	e.cfg.Funnel.Receive(item)
	e.signal()
}

// hashContent returns the hex-encoded SHA-256 of the inbox content.
// Stable across runs, cheap (~µs for the panel-route prefix + body),
// and short enough that the truncated form ([:12]) is unique in any
// realistic engine.Receive trace volume.
func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
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
