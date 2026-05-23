package engine

import (
	bridle "github.com/CarriedWorldUniverse/bridle"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

type UIHook struct {
	Program *tea.Program
}

func (h *UIHook) BeginTurn(turnID, label, model, provider string, triggerMsg int64) {
	// TurnStarted is emitted by AgoraReturnHandler.OnTurnStart, which
	// has the source + msg_id context this hook doesn't.
}

func (h *UIHook) OnBridleEvent(ev bridle.Event) {
	switch e := ev.(type) {
	case bridle.ModelChunk:
		h.Program.Send(ui.TurnChunk{Text: e.Text})
	case bridle.TurnDone:
		h.Program.Send(ui.TurnDone{})
	}
}

func (h *UIHook) EndTurn() {}
