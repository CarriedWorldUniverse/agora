// AgoraReturnHandler implements funnel.ReturnHandler for agora's
// dual-channel routing: chat-source replies go to nexus bus,
// tty-source replies stay in the local TUI panel.
//
// NEX-82 — agora consumes funnel.Funnel as the deliberation engine
// (compaction, session resolver, filter, observability all free).
// The ReturnHandler is where agora plugs in its source-aware routing
// without having to re-implement the rest of the engine.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"

	"github.com/CarriedWorldUniverse/agora/internal/bus"
	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

// SourceChat and SourceTTY are the two trigger sources agora knows
// about. They match what we set on bridle.InboxItem.Source when
// pushing into the funnel; funnel propagates them into
// TurnTrigger.Source via NEX-89.
const (
	SourceChat = "chat"
	SourceTTY  = "tty"
)

// AgoraReturnHandler routes Deliberate results by trigger source.
// Constructed once per agora process; passed as funnel.Config.Return.
type AgoraReturnHandler struct {
	Bus     *bus.Bus
	Program *tea.Program
	Logger  *slog.Logger
}

// OnTurnStart fires when the funnel pops an inbox item and is about
// to invoke the provider. Currently a no-op; future enhancement
// could surface a per-source "agent is responding..." spinner state
// in the TUI.
func (h *AgoraReturnHandler) OnTurnStart(ctx context.Context, t funnel.TurnTrigger) error {
	if h.Logger != nil {
		h.Logger.Debug("return handler: turn start",
			"source", t.Source,
			"msg_id", t.MsgID,
			"from", t.From)
	}
	return nil
}

// Handle routes the deliberation result. notify-operator fenced
// blocks are stripped post-hoc (NEX-63 carry-over) and surfaced via
// the panel; the cleaned reply goes to the channel matching the
// trigger's Source.
func (h *AgoraReturnHandler) Handle(ctx context.Context, res funnel.DeliberateResult, t funnel.TurnTrigger) error {
	reply := res.TurnResult.FinalText
	if reply == "" {
		// Empty FinalText is legitimate (filter suppressed, model
		// declined, tool-only turn). Nothing to surface either way.
		return nil
	}

	// Strip + route notify-operator fenced blocks before dispatching
	// the rest. Same post-hoc parse path that lived in the previous
	// engine.handle (NEX-63).
	notifications, cleaned := extractNotifyBlocks(reply)
	for _, n := range notifications {
		h.Program.Send(ui.NotifyOperator{Body: n})
	}
	reply = cleaned
	if reply == "" {
		// Reply was entirely notify-operator content; nothing left
		// to route after stripping. Common for status-update turns.
		return nil
	}

	switch t.Source {
	case SourceTTY:
		// tty-source: operator typed it, reply stays in panel only.
		// Never goes to the bus.
		h.Program.Send(ui.ChatPanelReply{Body: reply})

	case SourceChat, "":
		// chat-source (or legacy empty default): reply to nexus chat,
		// threaded on the triggering message.
		if _, err := h.Bus.SendChat(ctx, reply, t.MsgID, ""); err != nil {
			if h.Logger != nil {
				h.Logger.Error("return handler: send chat reply failed",
					"reply_to", t.MsgID,
					"err", err)
			}
			h.Program.Send(ui.EngineError{
				Source: SourceChat,
				Error:  fmt.Sprintf("send to nexus: %v", err),
			})
			return err
		}
		// Mirror the outgoing chat into the panel so the operator
		// can see what we replied with without reading it back from
		// the bus.
		h.Program.Send(ui.ChatSent{To: t.From, Body: reply})

	default:
		// Unknown source — treat as chat-only-panel to be safe
		// (don't accidentally surface to bus on misconfiguration).
		if h.Logger != nil {
			h.Logger.Warn("return handler: unknown trigger source — routing to panel",
				"source", t.Source)
		}
		h.Program.Send(ui.ChatPanelReply{Body: reply})
	}

	return nil
}

// Compile-time check.
var _ funnel.ReturnHandler = (*AgoraReturnHandler)(nil)

// Force "time" to stay imported for future TurnTrigger-based
// timestamp work; otherwise vet flags it.
var _ = time.Now
