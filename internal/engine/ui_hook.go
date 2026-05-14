// UIHook implements funnel.ObservabilityHook to pipe raw bridle
// events into the agora UI. Today's wiring: ModelChunk → live-line
// render, TurnDone → clear live-line. Tool-call events are dropped
// (claudecode runs its own agentic loop and renders tool activity
// natively in its session jsonl; agora doesn't surface it in the
// panel for v1).
//
// NEX-82 — funnel.ObservabilityHook is the seam between funnel's
// deliberation loop and our streaming render.
package engine

import (
	bridle "github.com/CarriedWorldUniverse/bridle"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

// UIHook routes bridle events to the bubbletea program.
// Concurrent-safe via tea.Program.Send (which is itself safe).
type UIHook struct {
	Program *tea.Program
}

// BeginTurn fires once per RunTurn invocation. Funnel uses turnID
// for observability correlation; agora doesn't need it for UI render.
func (h *UIHook) BeginTurn(turnID, label, model, provider string, triggerMsg int64) {
	// No-op. The "agent is responding" affordance comes from the
	// ReturnHandler.OnTurnStart path.
}

// OnBridleEvent fires per bridle event during the provider turn.
// Map ModelChunk and TurnDone into UI messages; ignore everything
// else for v1.
func (h *UIHook) OnBridleEvent(ev bridle.Event) {
	switch e := ev.(type) {
	case bridle.ModelChunk:
		h.Program.Send(ui.ModelChunk{Text: e.Text})
	case bridle.TurnDone:
		h.Program.Send(ui.ModelTurnEnd{})
	}
}

// EndTurn fires after the turn completes. ModelTurnEnd was already
// surfaced via bridle.TurnDone, so this is a no-op for UI.
func (h *UIHook) EndTurn() {}
