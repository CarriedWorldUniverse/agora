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
	"os"
	"path/filepath"
	"strings"
	"sync"

	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/google/uuid"

	"github.com/CarriedWorldUniverse/agora/internal/inbox"
)

// sessionNamespace mirrors funnel.sessionNamespace so agora-derived
// session ids share the same UUID space as the funnel — same aspect,
// same thread, same jsonl on disk regardless of which harness drove
// the turn. Derived (not hardcoded) so the value is reproducible
// from the name string and not bound to any single operator's UUID.
//
// TODO(NEX-46.x): swap this whole derivation for funnel.SessionResolver
// rather than duplicating the namespace + derivation logic. Today we
// don't import nexus/frame/funnel from agora because it pulls in
// heavier deps; revisit once we want per-thread session isolation
// (the resolver also tracks the New/Resumed flag the harness needs).
var sessionNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("nexus.funnel.session.v1"))

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
		if isNew && sessionJSONLExists(cfg.Cwd, sid) {
			// jsonl from a prior agora process exists on disk →
			// claudecode should --resume, not --session-id (which
			// works against an existing id but won't load the
			// prior turn history). NEX-65.
			isNew = false
		}
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

// deriveSessionID returns a stable per-aspect session id, as a
// uuid_v5 string (claude-code rejects non-UUID session ids).
//
// Global-context mode (operator preference 2026-05-15): one session
// per aspect, regardless of source. Chat-source and tty-source turns
// share the same jsonl on disk, so the model carries coherent context
// across both channels. Matches the ContextGlobal mode reported on
// the register frame in bus.go.
//
// The inbox.Item is accepted for forward-compatibility (per-thread
// or per-source modes can branch on it without changing the call
// site) but currently ignored.
func deriveSessionID(aspectID string, _ inbox.Item) string {
	return uuid.NewSHA1(sessionNamespace, []byte(aspectID)).String()
}

// sessionJSONLExists reports whether claude-code has an on-disk
// jsonl for the given session id under the project directory it
// would derive from cwd. Used at first-turn time to decide whether
// to pass SessionHandle{New:true} (fresh) or {New:false} (resume).
//
// claude-code's projects directory layout:
//
//	~/.claude/projects/<sanitized-cwd>/<session-id>.jsonl
//
// where sanitized-cwd is the absolute cwd with path separators
// replaced by '-' (a leading '-' falls out naturally since the
// absolute path starts with '/'). Empty cwd → use process cwd.
func sessionJSONLExists(cwd, sid string) bool {
	resolved := cwd
	if resolved == "" {
		wd, err := os.Getwd()
		if err != nil {
			return false
		}
		resolved = wd
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	sanitized := strings.ReplaceAll(abs, string(filepath.Separator), "-")
	path := filepath.Join(home, ".claude", "projects", sanitized, sid+".jsonl")
	_, err = os.Stat(path)
	return err == nil
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
