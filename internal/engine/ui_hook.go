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
		// The raw stream that filled the inline UI block includes any
		// notify-operator fence. Strip it engine-side (ui can't import
		// engine) and hand the cleaned text + a notify flag to the UI so
		// it can reconcile the inline block, avoiding a double render of
		// the notify body (inline AND as the red blockNotify).
		notifications, cleaned := extractNotifyBlocks(e.Result.FinalText)
		h.Program.Send(ui.TurnDone{FinalText: cleaned, HadNotify: len(notifications) > 0})
	}
}

func (h *UIHook) EndTurn() {}
