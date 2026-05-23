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

	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"

	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

// busSender is the subset of bus.Bus that AgoraReturnHandler uses.
// Keeping the dependency interface-typed lets tests inject a fake
// without standing up a websocket.
type busSender interface {
	SendChat(ctx context.Context, content string, replyTo int64, topic string) (int64, error)
}

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
	Bus     busSender
	Program *tea.Program
	Logger  *slog.Logger
}

// OnTurnStart fires when the funnel pops an inbox item and is about
// to invoke the provider. Currently a no-op; future enhancement
// could surface a per-source "agent is responding..." spinner state
// in the TUI.
func (h *AgoraReturnHandler) OnTurnStart(ctx context.Context, t funnel.TurnTrigger) error {
	if h.Logger != nil {
		// NEX-250 dup investigation: Info-level so we can correlate
		// engine.Receive → OnTurnStart → Handle for each submission.
		h.Logger.Info("return handler: turn start",
			"source", t.Source,
			"msg_id", t.MsgID,
			"from", t.From)
	}
	if h.Program != nil {
		h.Program.Send(ui.TurnStarted{Source: t.Source, MsgID: t.MsgID})
	}
	return nil
}

// Handle routes the deliberation result. notify-operator fenced
// blocks are stripped post-hoc (NEX-63 carry-over) and surfaced via
// the panel; the cleaned reply goes to the channel matching the
// trigger's Source.
func (h *AgoraReturnHandler) Handle(ctx context.Context, res funnel.DeliberateResult, t funnel.TurnTrigger) error {
	reply := res.TurnResult.FinalText
	if h.Logger != nil {
		excerpt := reply
		if len(excerpt) > 80 {
			excerpt = excerpt[:80] + "…"
		}
		h.Logger.Info("return handler: handle",
			"source", t.Source,
			"msg_id", t.MsgID,
			"from", t.From,
			"reply_len", len(reply),
			"excerpt", excerpt)
	}

	// Strip notify-operator blocks and surface them; the cleaned reply
	// (if any) is what would have routed. Notifications still fire as
	// distinct blockNotify entries.
	notifications, cleaned := extractNotifyBlocks(reply)
	for _, n := range notifications {
		if h.Program != nil {
			h.Program.Send(ui.NotifyOperator{Body: n})
		}
	}
	reply = cleaned

	if reply == "" {
		// Nothing to route. Active streaming block (if any) was already
		// finalised by TurnDone. Nothing to do here.
		return nil
	}

	switch t.Source {
	case SourceTTY:
		// Active streaming block carries the panel reply. Nothing
		// additional to send to UI.
		return nil

	case SourceChat, "":
		// Wire emission to nexus chat. UI render already happened via
		// the streaming block; no ChatSent mirror needed.
		if _, err := h.Bus.SendChat(ctx, reply, t.MsgID, ""); err != nil {
			if h.Logger != nil {
				h.Logger.Error("return handler: send chat reply failed",
					"reply_to", t.MsgID,
					"err", err)
			}
			if h.Program != nil {
				h.Program.Send(ui.TurnFailed{Reason: fmt.Sprintf("send to nexus: %v", err)})
			}
			return err
		}
		return nil

	default:
		if h.Logger != nil {
			h.Logger.Warn("return handler: unknown trigger source — treating as panel-only",
				"source", t.Source)
		}
		return nil
	}
}

// Compile-time check.
var _ funnel.ReturnHandler = (*AgoraReturnHandler)(nil)
