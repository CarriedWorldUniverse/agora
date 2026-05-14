// Package engine drives the per-turn dispatch loop.
//
// Spec §8 — output routing by Source tag:
//
//   - inbox.SourceChat → reply goes to nexus chat via bus.SendChat,
//     threaded on the original message (reply_to = item.MsgID).
//   - inbox.SourceTTY  → reply renders in the chat panel only,
//     via a ChatPanelReply tea.Msg sent to the bubbletea program.
//
// The actual model invocation is a pluggable TurnFunc. NEX-55 ships
// with a stub that acknowledges the input — NEX-58/59 swap in the
// bridle/claudecode subprocess driver. Routing is settled now so the
// engine implementation can drop in without disturbing the shell.
package engine

import (
	"context"
	"fmt"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/agora/internal/bus"
	"github.com/CarriedWorldUniverse/agora/internal/inbox"
	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

// TurnFunc is the per-turn model invocation. Takes an inbox item,
// returns the assistant's reply text (or an error). NEX-55 wires a
// stub; NEX-58/59 swap in a bridle-backed implementation.
type TurnFunc func(ctx context.Context, it inbox.Item) (string, error)

// Config bundles dependencies for the engine. All fields are
// required.
type Config struct {
	Inbox   *inbox.Inbox
	Bus     *bus.Bus
	Program *tea.Program
	Logger  *slog.Logger
	Turn    TurnFunc
}

// Engine is the dispatch loop. One per agora process.
type Engine struct {
	cfg Config
}

// New builds an Engine with the given config.
func New(cfg Config) *Engine {
	return &Engine{cfg: cfg}
}

// Run blocks until ctx is cancelled. Each Updates wake-up drains
// the inbox synchronously (one item per turn).
func (e *Engine) Run(ctx context.Context) {
	e.cfg.Logger.Info("engine running")
	for {
		select {
		case <-ctx.Done():
			e.cfg.Logger.Info("engine stopping", "err", ctx.Err())
			return
		case <-e.cfg.Inbox.Updates():
			e.drain(ctx)
		}
	}
}

// drain pops every queued item and processes it in order. Each turn
// runs to completion before the next starts (per-turn synchronicity
// matches the per-turn engine model — no interleaving). After
// draining we notify the UI so the status-line depth counter
// reflects the new state.
func (e *Engine) drain(ctx context.Context) {
	for {
		it, ok := e.cfg.Inbox.Take()
		if !ok {
			break
		}
		e.handle(ctx, it)
	}
	e.cfg.Program.Send(ui.InboxUpdated{})
}

// handle runs one turn + routes the reply by Source tag.
func (e *Engine) handle(ctx context.Context, it inbox.Item) {
	reply, err := e.cfg.Turn(ctx, it)
	if err != nil {
		e.cfg.Logger.Error("turn failed",
			"source", it.Source,
			"from", it.From,
			"err", err)
		// Surface to the chat panel so the operator sees what
		// happened. Chat-source failures still get a chat-panel
		// notification (not a bus reply) because we don't want to
		// shove model errors at the rest of the cluster.
		e.cfg.Program.Send(ui.EngineError{
			Source: string(it.Source),
			Error:  err.Error(),
		})
		return
	}

	switch it.Source {
	case inbox.SourceChat:
		if _, sendErr := e.cfg.Bus.SendChat(ctx, reply, it.MsgID, ""); sendErr != nil {
			e.cfg.Logger.Error("send chat reply failed",
				"reply_to", it.MsgID,
				"err", sendErr)
			// Surface in the panel so the operator knows.
			e.cfg.Program.Send(ui.EngineError{
				Source: "chat",
				Error:  fmt.Sprintf("send to nexus: %v", sendErr),
			})
			return
		}
		// Mirror the outgoing chat into the panel so the operator can
		// see what we replied with, without needing to read it back
		// from the bus.
		e.cfg.Program.Send(ui.ChatSent{
			To:   it.From,
			Body: reply,
		})

	case inbox.SourceTTY:
		e.cfg.Program.Send(ui.ChatPanelReply{Body: reply})
	}
}

// StubTurn is the NEX-55 placeholder TurnFunc. Echoes a fixed
// acknowledgement so the routing path can be validated end-to-end
// without a model. Swap to bridle in NEX-58/59.
func StubTurn(ctx context.Context, it inbox.Item) (string, error) {
	return fmt.Sprintf("[stub engine] received via %s: %q", it.Source, it.Content), nil
}
