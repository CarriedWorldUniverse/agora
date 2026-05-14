// Bridle-backed TurnFunc — NEX-59 dogfood swap.
//
// Replaces StubTurn with a real per-turn claude-code invocation via
// bridle. Streams ModelChunk events through TurnContext.EmitChunk so
// the TUI's live-line render reflects the model's output in real
// time, then returns the canonical reply text for routing.
//
// Session model (v0): one session per (aspect, source) pair —
// tty-sourced turns share one session id, chat-sourced turns share
// another. This gives the model a continuous conversation on each
// channel without per-thread fragmentation. NEX-46.x lands the
// per-thread session derivation matching the spec §4 invariant.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	bridle "github.com/CarriedWorldUniverse/bridle"

	"github.com/CarriedWorldUniverse/agora/internal/inbox"
)

// BridleConfig bundles what the bridle-backed TurnFunc needs.
type BridleConfig struct {
	Provider         bridle.Provider
	ProviderID       bridle.ProviderID
	Model            string
	AspectID         string
	Cwd              string // working dir for claude-code; affects its .mcp.json + jsonl path
	SystemPrompt     string // optional system prompt to append (composed elsewhere)
	MaxStepsPerTurn  int    // 0 = unlimited
}

// chunkSink is the bridle.EventSink agora installs per turn. Only
// ModelChunk is surfaced to the TUI today — tool-call events would
// be visible via claude-code's own surface, and the eventual
// AfterToolCall hook (when bridle wires it) lands as a separate
// rendering pass.
type chunkSink struct {
	tc TurnContext
}

func (s chunkSink) Emit(e bridle.Event) {
	if c, ok := e.(bridle.ModelChunk); ok {
		s.tc.EmitChunk(c.Text)
	}
}

// noopRunner satisfies bridle.ToolRunner without serving any tools.
// claudecode is subprocess-stream and doesn't ask the runner for
// anything (SupportsCustomTools=false), so the runner is unreachable
// in practice — present only to keep the harness contract.
type noopRunner struct{}

func (noopRunner) Run(ctx context.Context, call bridle.ToolCall) (json.RawMessage, error) {
	return nil, fmt.Errorf("agora: tool runner not implemented; call=%s", call.Name)
}

// NewBridleTurn returns a TurnFunc that drives one bridle.RunTurn
// per inbox item. Tracks per-session New/continue state internally
// so the first turn for a given session id passes New=true and all
// subsequent turns pass New=false (claude-code uses this to choose
// between --session-id and --resume).
func NewBridleTurn(cfg BridleConfig) TurnFunc {
	if cfg.Provider == nil {
		panic("agora: BridleConfig.Provider is nil")
	}
	harness := bridle.NewHarness(cfg.Provider)
	runner := noopRunner{}

	var (
		mu         sync.Mutex
		seenSessions = map[string]bool{}
	)

	return func(ctx context.Context, tc TurnContext, it inbox.Item) (string, error) {
		sid := deriveSessionID(cfg.AspectID, it)

		mu.Lock()
		isNew := !seenSessions[sid]
		seenSessions[sid] = true
		mu.Unlock()

		req := bridle.TurnRequest{
			AspectID:           cfg.AspectID,
			AppendSystemPrompt: cfg.SystemPrompt,
			Session:            bridle.SessionHandle{ID: sid, New: isNew},
			UserMessage:        renderUserMessage(it),
			Provider:           cfg.ProviderID,
			Model:              cfg.Model,
			Cwd:                cfg.Cwd,
			MaxSteps:           cfg.MaxStepsPerTurn,
		}

		res, err := harness.RunTurn(ctx, req, runner, chunkSink{tc: tc})
		if err != nil {
			return "", fmt.Errorf("bridle run: %w", err)
		}
		return res.FinalText, nil
	}
}

// deriveSessionID returns a stable per-source session id. v0:
//   - tty:  "agora-tty-<aspect>"
//   - chat: "agora-chat-<aspect>"
//
// Per-thread session derivation (uuid_v5 of thread_root) is deferred
// to a follow-up; for the dogfood window, one chat session and one
// tty session per aspect is enough to validate the engine path.
func deriveSessionID(aspectID string, it inbox.Item) string {
	return fmt.Sprintf("agora-%s-%s", it.Source, aspectID)
}

// renderUserMessage shapes the inbox item into the prompt body for
// the model. Chat-source items include the From + (if present) Reason
// so the model has the routing context; tty-source items pass the
// content verbatim.
func renderUserMessage(it inbox.Item) string {
	switch it.Source {
	case inbox.SourceChat:
		header := fmt.Sprintf("[chat from %s", it.From)
		if it.Reason != "" {
			header += " · " + it.Reason
		}
		header += "]"
		return header + "\n\n" + it.Content
	default:
		return it.Content
	}
}
