// Package harness is the agora RESEARCH turn loop. Since the ctxmap
// migration it is a thin host over bridle/ctxmap/memory.Engine: the engine
// owns all memory behavior (assembly blocks, retrieval, reconciliation,
// extraction, recall/inspect); this package owns what a host owns — the
// provider call, the tool loop, the raw tail + assembly budget, and the
// transcript ground truth. The bench drives this loop, so every bench run
// exercises the real runtime (bridle) memory code.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/backend"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/memory"
)

type Config struct {
	SystemPrompt string
	Model        string // backend model id, e.g. "ornith"
	MaxSteps     int    // tool-loop cap per turn
	TailTurns    int    // raw transcript tail length (turns)
	MapEnabled   bool   // ablation switch (control MCP: map on/off)
	// AssemblyBudget caps the assembled prompt (system + map blocks + tail +
	// message) in approximate tokens (chars/4). All bench targets share one
	// budget (spec fairness rule); the tail gets whatever the fixed blocks
	// don't use, oldest turns dropped first (naive truncation — this IS the
	// standard comparator's compaction). 0 = default 200k.
	AssemblyBudget int
	TranscriptPath string // jsonl ground truth; empty = no file
}

type TurnRecord struct {
	N         int    `json:"n"`
	User      string `json:"user"`
	Assistant string `json:"assistant"`
	// Injected is the subgraph block inserted before this turn's user message
	// — recorded for audit only, never replayed (cache stability is a
	// within-turn property).
	Injected string `json:"injected,omitempty"`
	Time     string `json:"time"`
}

// TurnResult is what the control MCP's prompt() returns.
type TurnResult struct {
	Answer       string
	TurnN        int
	Notices      []string
	InputTokens  int
	OutputTokens int
	CachedTokens int // subset of InputTokens served from prefix cache
	RecallCalls  int
}

type Session struct {
	mu         sync.Mutex
	cfg        Config
	provider   backend.Provider
	eng        *memory.Engine // nil = map-off
	tools      map[string]memory.Tool
	transcript []TurnRecord
	sessionID  string
}

type discardSink struct{}

func (discardSink) Emit(backend.Event) {}

// NewSession creates a research-harness session. eng may be nil (map-off /
// standard-comparator mode).
func NewSession(cfg Config, p backend.Provider, eng *memory.Engine) *Session {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 6
	}
	if cfg.TailTurns == 0 {
		cfg.TailTurns = 8
	}
	if cfg.AssemblyBudget == 0 {
		cfg.AssemblyBudget = 200_000
	}
	s := &Session{cfg: cfg, provider: p, eng: eng,
		tools:     map[string]memory.Tool{},
		sessionID: fmt.Sprintf("ctx_%d", time.Now().UnixMilli())}
	if eng != nil {
		s.sessionID = eng.SessionID()
		for _, t := range eng.Tools() {
			s.tools[t.Name] = t
		}
	}
	return s
}

func (s *Session) ID() string { return s.sessionID }

// Close drains the engine's extraction queue.
func (s *Session) Close() {
	if s.eng != nil {
		s.eng.Close()
	}
}

// WaitExtraction blocks until queued extractions land (bench probes need a
// settled store).
func (s *Session) WaitExtraction() []string {
	if s.eng == nil {
		return nil
	}
	return s.eng.WaitExtraction()
}

// Preview renders the next turn's memory blocks for a hypothetical message.
func (s *Session) Preview(msg string) string {
	if s.eng == nil {
		return "(map disabled)"
	}
	return s.eng.Preview(msg)
}

// Turn runs one full harness turn: assemble → provider tool loop → record.
func (s *Session) Turn(ctx context.Context, userMsg string) (*TurnResult, error) {
	s.mu.Lock()
	turnN := len(s.transcript) + 1

	var blocks []string
	if s.cfg.SystemPrompt != "" {
		blocks = append(blocks, s.cfg.SystemPrompt)
	}
	injected := ""
	var renderedIDs []string
	var notices []string
	if s.cfg.MapEnabled && s.eng != nil {
		b := s.eng.AssembleBlocks(userMsg, turnN)
		blocks = append(blocks, b.Framing, b.Core)
		injected = b.Subgraph
		renderedIDs = b.RenderedIDs
		notices = b.Notices
	}
	fixedTok := approxTokens(strings.Join(blocks, "\n\n")) + approxTokens(injected) + approxTokens(userMsg)
	msgs := s.tailMessages(s.cfg.AssemblyBudget - fixedTok)
	s.mu.Unlock()

	if injected != "" {
		msgs = append(msgs, backend.ProviderMessage{Role: "user", Content: injected})
	}
	msgs = append(msgs, backend.ProviderMessage{Role: "user", Content: userMsg})

	req := backend.ProviderRequest{
		AppendSystemPrompt: strings.Join(blocks, "\n\n"),
		Messages:           msgs,
		Model:              s.cfg.Model,
		MaxSteps:           s.cfg.MaxSteps,
	}
	if s.cfg.MapEnabled && s.eng != nil {
		for _, t := range s.eng.Tools() {
			req.Tools = append(req.Tools, backend.ToolDef{
				Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
			})
		}
	}

	res := &TurnResult{TurnN: turnN, Notices: notices}
	var finalText string
	exhausted := true
	for step := 0; step < s.cfg.MaxSteps; step++ {
		pr, err := s.provider.RunTurn(ctx, req, discardSink{})
		if err != nil {
			return nil, err
		}
		res.InputTokens += pr.Usage.InputTokens
		res.OutputTokens += pr.Usage.OutputTokens
		res.CachedTokens += pr.Usage.CacheReadInputTokens
		if len(pr.ToolCalls) == 0 {
			finalText = pr.FinalText
			exhausted = false
			break
		}
		asst := backend.ProviderMessage{Role: "assistant", Content: pr.FinalText, ToolCalls: pr.ToolCalls}
		req.Messages = append(req.Messages, asst)
		for _, tc := range pr.ToolCalls {
			out := "unknown tool: " + tc.Name
			if t, ok := s.tools[tc.Name]; ok {
				out = t.Run(tc.Args)
			}
			res.RecallCalls++
			req.Messages = append(req.Messages, backend.ProviderMessage{
				Role: "tool_result", ToolCallID: tc.ID, ToolName: tc.Name, Content: out,
			})
		}
	}
	// tool-budget exhaustion must still yield an answer
	if exhausted && strings.TrimSpace(finalText) == "" {
		req.Tools = nil
		if pr, err := s.provider.RunTurn(ctx, req, discardSink{}); err == nil {
			finalText = pr.FinalText
			res.InputTokens += pr.Usage.InputTokens
			res.OutputTokens += pr.Usage.OutputTokens
			res.CachedTokens += pr.Usage.CacheReadInputTokens
		}
	}

	s.mu.Lock()
	rec := TurnRecord{N: turnN, User: userMsg, Assistant: finalText, Injected: injected, Time: time.Now().UTC().Format(time.RFC3339)}
	s.transcript = append(s.transcript, rec)
	s.appendTranscript(rec)
	s.mu.Unlock()

	if s.cfg.MapEnabled && s.eng != nil {
		s.eng.RecordTurn(turnN, userMsg, finalText, renderedIDs)
	}

	res.Answer = finalText
	return res, nil
}

func approxTokens(s string) int { return len(s)/4 + 1 }

func (s *Session) tailMessages(budget int) []backend.ProviderMessage {
	start := len(s.transcript) - s.cfg.TailTurns
	if start < 0 {
		start = 0
	}
	kept := 0
	spend := 0
	for i := len(s.transcript) - 1; i >= start; i-- {
		t := s.transcript[i]
		cost := approxTokens(t.User) + approxTokens(t.Assistant)
		if spend+cost > budget {
			break
		}
		spend += cost
		kept++
	}
	var msgs []backend.ProviderMessage
	for _, t := range s.transcript[len(s.transcript)-kept:] {
		msgs = append(msgs, backend.ProviderMessage{Role: "user", Content: t.User})
		msgs = append(msgs, backend.ProviderMessage{Role: "assistant", Content: t.Assistant})
	}
	return msgs
}

func (s *Session) appendTranscript(rec TurnRecord) {
	if s.cfg.TranscriptPath == "" {
		return
	}
	f, err := os.OpenFile(s.cfg.TranscriptPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	json.NewEncoder(f).Encode(rec)
}
