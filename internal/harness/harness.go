// Package harness is the ctxmap turn loop (spec §2.1):
//
//	assemble (core + subgraph + tail) → backend → answer out
//	→ turn delta to the in-process extractor on a background worker
//	→ reconcile-lite (dedup, contradiction candidates) → store
//
// Extraction NEVER blocks a turn (invariant 6): a dead or slow extractor
// degrades the harness to plain-transcript behavior.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/backend"
	"github.com/CarriedWorldUniverse/agora/internal/extractor"
	"github.com/CarriedWorldUniverse/agora/internal/render"
	"github.com/CarriedWorldUniverse/agora/internal/store"
)

// Extract is the extractor seam the harness needs; *extractor.Extractor
// satisfies it. A nil Proposer disables extraction (map-off ablation).
type Proposer interface {
	Propose(current extractor.Turn, ctx []extractor.Turn, glossary map[string]string) ([]extractor.FactProposal, error)
}

// PairJudge classifies the relation between two same-topic statements
// (SAME / CONTRADICTS / DISTINCT). *extractor.Extractor satisfies it.
type PairJudge interface {
	JudgePair(a, b string) (extractor.PairVerdict, error)
}

// Embedder produces L2-normalized sentence vectors. Optional: nil falls back
// to token-overlap reconciliation.
type Embedder interface {
	Embed(text string) ([]float32, error)
}

type Config struct {
	SystemPrompt string
	Model        string // backend model id, e.g. "ornith"
	MaxSteps     int    // tool-loop cap per turn
	TailTurns    int    // raw transcript tail length (turns)
	ContextTurns int    // turns of context handed to the extractor (spec: 2)
	MapEnabled   bool   // ablation switch (control MCP: map on/off)
	// AssemblyBudget caps the assembled prompt (system + map blocks + tail +
	// message) in approximate tokens (chars/4). All bench targets share one
	// budget (spec §9.1 fairness rule); the tail gets whatever the fixed
	// blocks don't use, oldest turns dropped first (naive truncation — this
	// IS the standard comparator's compaction). 0 = default 200k.
	AssemblyBudget int
	TranscriptPath string // jsonl ground truth; empty = no file
}

type TurnRecord struct {
	N         int    `json:"n"`
	User      string `json:"user"`
	Assistant string `json:"assistant"`
	// Injected is the working-memory subgraph block that was inserted before
	// this turn's user message. It is REPLAYED verbatim in the tail so every
	// request is byte-identical to the previous request plus a suffix —
	// prefix-cache stability (spec invariant 4). Churn lives at the END of
	// the prompt, never in the system block.
	Injected string `json:"injected,omitempty"`
	Time     string `json:"time"`
}

// TurnResult is what the control MCP's prompt() returns (spec §7).
type TurnResult struct {
	Answer         string
	TurnN          int
	FactsExtracted []string // ids asserted this turn (arrives async; see WaitExtraction)
	Notices        []string
	InputTokens    int
	OutputTokens   int
	RecallCalls    int
}

type Session struct {
	mu        sync.Mutex
	cfg       Config
	provider  backend.Provider
	st        *store.Store
	rend      *render.Renderer
	prop      Proposer
	transcript []TurnRecord
	sessionID string

	judge    PairJudge // optional; nil = no pair judgment (token heuristics only)
	embedder Embedder  // optional; nil = token-overlap reconciliation

	extractQ   chan int // turn numbers pending extraction
	wg         sync.WaitGroup
	lastIDs    []string // facts asserted by the most recent completed extraction
	pending    int      // extractions enqueued but not yet completed
}

// memoryFraming tells the model that working memory is automatic. Prepended to
// the assembled prompt on map-enabled turns, ahead of the (stable) core block.
const memoryFraming = `## Working memory (automatic)
Durable facts from this conversation are captured for you automatically in the background — you never save, persist, or write anything yourself, and you have no tool to do so. Facts already known appear under "Working memory" below; treat them as established context. Just converse naturally: when the user tells you something, respond to its substance — do not acknowledge it as "saved" or apologize for being unable to save it. Use the ` + "`recall`" + ` tool ONLY when you need older context that is not visible in the prompt, and ` + "`inspect`" + ` only to check a fact's evidence. Never call them just to verify that something was stored.`

// toolDescription for recall reflects the automatic-memory framing.

type discardSink struct{}

func (discardSink) Emit(backend.Event) {}

func NewSession(cfg Config, p backend.Provider, st *store.Store, rend *render.Renderer, prop Proposer) *Session {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 6
	}
	if cfg.TailTurns == 0 {
		cfg.TailTurns = 8
	}
	if cfg.ContextTurns == 0 {
		cfg.ContextTurns = 2
	}
	if cfg.AssemblyBudget == 0 {
		cfg.AssemblyBudget = 200_000 // inside a 256k window with headroom for output
	}
	s := &Session{
		cfg: cfg, provider: p, st: st, rend: rend, prop: prop,
		sessionID: fmt.Sprintf("ctx_%d", time.Now().UnixMilli()),
		extractQ:  make(chan int, 64),
	}
	s.wg.Add(1)
	go s.extractWorker()
	return s
}

// SetReconciler wires the embedding-based reconciler (both optional; the
// extractor usually doubles as the PairJudge).
func (s *Session) SetReconciler(e Embedder, j PairJudge) {
	s.embedder = e
	s.judge = j
}

func (s *Session) ID() string { return s.sessionID }

// Close drains the extraction queue and stops the worker.
func (s *Session) Close() {
	close(s.extractQ)
	s.wg.Wait()
}

// WaitExtraction blocks until all queued extractions have landed — bench
// probes call this so expect_fact_recalled sees a settled store.
func (s *Session) WaitExtraction() []string {
	for {
		s.mu.Lock()
		empty := s.pending == 0
		ids := append([]string{}, s.lastIDs...)
		s.mu.Unlock()
		if empty {
			return ids
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Turn runs one full harness turn.
func (s *Session) Turn(ctx context.Context, userMsg string) (*TurnResult, error) {
	s.mu.Lock()
	turnN := len(s.transcript) + 1

	// 1. assemble — cache-friendly layout (invariant 4): stable prefix first
	// (system + epoch-frozen core), append-only tail next, ALL per-turn churn
	// (subgraph block) at the END, immediately before the user message.
	var blocks []string
	if s.cfg.SystemPrompt != "" {
		blocks = append(blocks, s.cfg.SystemPrompt)
	}
	var renderedIDs []string
	var notices []string
	injected := ""
	if s.cfg.MapEnabled {
		// memory framing: the model must treat working memory as AUTOMATIC and
		// invisible. Without this it reads recall/inspect as "the only memory
		// tools I have", fixates on being unable to "save" facts, and burns
		// turns apologizing (found by native driving 2026-07-08). It should just
		// converse; the harness persists facts in the background.
		blocks = append(blocks, memoryFraming)
		blocks = append(blocks, s.rend.RenderCore()) // byte-stable within an epoch
		seeds := s.retrieve(userMsg)
		sub, ids := s.rend.RenderSubgraph(seeds)
		renderedIDs = ids
		if strings.TrimSpace(sub) != "" {
			injected = sub
		}
		for _, line := range strings.Split(sub, "\n") {
			if strings.Contains(line, "NOTICE:") {
				notices = append(notices, strings.TrimPrefix(strings.TrimSpace(line), "- "))
			}
		}
	}
	// assembly budget: fixed blocks + churn + current message spend first;
	// the tail gets the remainder, oldest turns dropped first.
	fixedTok := approxTokens(strings.Join(blocks, "\n\n")) + approxTokens(injected) + approxTokens(userMsg)
	msgs := s.tailMessages(s.cfg.AssemblyBudget - fixedTok)
	s.mu.Unlock()

	if injected != "" {
		msgs = append(msgs, backend.ProviderMessage{Role: "user", Content: injected})
	}
	msgs = append(msgs, backend.ProviderMessage{Role: "user", Content: userMsg})

	// 2. infer, harness-owned tool loop
	req := backend.ProviderRequest{
		AppendSystemPrompt: strings.Join(blocks, "\n\n"),
		Messages:           msgs,
		Model:              s.cfg.Model,
		MaxSteps:           s.cfg.MaxSteps,
	}
	if s.cfg.MapEnabled {
		req.Tools = s.toolDefs()
	}
	res := &TurnResult{TurnN: turnN, Notices: notices}
	var finalText string
	for step := 0; step < s.cfg.MaxSteps; step++ {
		pr, err := s.provider.RunTurn(ctx, req, discardSink{})
		if err != nil {
			return nil, err
		}
		res.InputTokens += pr.Usage.InputTokens
		res.OutputTokens += pr.Usage.OutputTokens
		if len(pr.ToolCalls) == 0 {
			finalText = pr.FinalText
			break
		}
		// execute recall/inspect against the store, append, continue
		asst := backend.ProviderMessage{Role: "assistant", Content: pr.FinalText, ToolCalls: pr.ToolCalls}
		req.Messages = append(req.Messages, asst)
		for _, tc := range pr.ToolCalls {
			out := s.runTool(tc)
			res.RecallCalls++
			req.Messages = append(req.Messages, backend.ProviderMessage{
				Role: "tool_result", ToolCallID: tc.ID, ToolName: tc.Name, Content: out,
			})
		}
	}

	// bookkeeping: rendered facts count toward reuse-confirmation
	s.mu.Lock()
	for _, id := range renderedIDs {
		s.st.RecordRender(id, turnN)
	}
	rec := TurnRecord{N: turnN, User: userMsg, Assistant: finalText, Injected: injected, Time: time.Now().UTC().Format(time.RFC3339)}
	s.transcript = append(s.transcript, rec)
	s.appendTranscript(rec)
	s.mu.Unlock()

	// 3. extract — async, never blocks
	if s.cfg.MapEnabled && s.prop != nil {
		select {
		case s.extractQ <- turnN:
			s.mu.Lock()
			s.pending++
			s.mu.Unlock()
		default: // queue full: drop to plain-transcript behavior rather than block
		}
	}

	res.Answer = finalText
	return res, nil
}

// ---- retrieval (fallback until the embedder lands): seeds from the message
// + active entities of the last N turns ----

var wordRe = regexp.MustCompile(`[a-zA-Z0-9][a-zA-Z0-9_-]{3,}`)

func (s *Session) retrieve(msg string) []*store.Fact {
	seen := map[string]bool{}
	var seeds []*store.Fact
	add := func(fs []*store.Fact) {
		for _, f := range fs {
			if !seen[f.ID] {
				seen[f.ID] = true
				seeds = append(seeds, f)
			}
		}
	}
	// seed source 1: content words of the current message (+ recent turns per spec)
	text := msg
	for i := len(s.transcript) - 1; i >= 0 && i >= len(s.transcript)-s.cfg.ContextTurns; i-- {
		text += " " + s.transcript[i].User
	}
	words := wordRe.FindAllString(text, 12)
	for _, w := range words {
		if fs, err := s.st.QueryText(w, 3); err == nil {
			add(fs)
		}
		if fs, err := s.st.QueryEntity(strings.ToLower(w), 3); err == nil {
			add(fs)
		}
	}
	if len(seeds) > 12 {
		seeds = seeds[:12]
	}
	return seeds
}

// RetrievePreview exposes retrieval seeding for the control MCP's
// render_preview (no model call, no RecordRender bookkeeping).
func (s *Session) RetrievePreview(msg string) []*store.Fact {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retrieve(msg)
}

// ---- tools served to the model ----

func (s *Session) toolDefs() []backend.ToolDef {
	return []backend.ToolDef{
		{
			Name:        "recall",
			Description: "Retrieve OLDER facts from earlier in this conversation that are not shown in the current prompt. Only needed when answering requires context beyond what is visible. Facts are stored automatically; this only reads them.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"what to search for"}},"required":["query"]}`),
		},
		{
			Name:        "inspect",
			Description: "Show the original transcript evidence behind one fact id (for auditing why a fact is believed). Rarely needed.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"fact_id":{"type":"string"}},"required":["fact_id"]}`),
		},
	}
}

func (s *Session) runTool(tc backend.ToolInvocation) string {
	var args struct {
		Query  string `json:"query"`
		FactID string `json:"fact_id"`
	}
	json.Unmarshal(tc.Args, &args)
	switch tc.Name {
	case "recall":
		s.mu.Lock()
		seeds := s.retrieve(args.Query)
		text, ids := s.rend.RenderSubgraph(seeds)
		turn := len(s.transcript) + 1
		for _, id := range ids {
			s.st.RecordRender(id, turn)
		}
		s.mu.Unlock()
		return text
	case "inspect":
		f, err := s.st.Get(args.FactID)
		if err != nil {
			return "no such fact: " + args.FactID
		}
		ev := "(transcript evidence unavailable)"
		s.mu.Lock()
		for _, sp := range f.Provenance {
			if sp.Turn-1 >= 0 && sp.Turn-1 < len(s.transcript) {
				t := s.transcript[sp.Turn-1]
				ev = fmt.Sprintf("turn %d — [user]: %s\n[assistant]: %s", sp.Turn, t.User, t.Assistant)
				break
			}
		}
		s.mu.Unlock()
		return fmt.Sprintf("[%s] %s (kind=%s status=%s trust=%s)\nEVIDENCE:\n%s", f.ID, f.Statement, f.Kind, f.Status, f.Trust, ev)
	}
	return "unknown tool: " + tc.Name
}

// ---- async extraction worker + reconcile-lite ----

func (s *Session) extractWorker() {
	defer s.wg.Done()
	for turnN := range s.extractQ {
		s.extractTurn(turnN)
		s.mu.Lock()
		s.pending--
		s.mu.Unlock()
	}
}

func (s *Session) extractTurn(turnN int) {
	s.mu.Lock()
	if turnN-1 >= len(s.transcript) {
		s.mu.Unlock()
		return
	}
	cur := s.transcript[turnN-1]
	// Defensive cap: the extractor sees dialogue, not bulk payloads. Oversized
	// turn text (pasted logs, padded bench turns) is head+tail truncated so the
	// extraction prompt always fits the extractor's context.
	cur.User = capText(cur.User, 4000)
	cur.Assistant = capText(cur.Assistant, 4000)
	var ctxTurns []extractor.Turn
	for i := turnN - 1 - s.cfg.ContextTurns; i < turnN-1; i++ {
		if i >= 0 {
			ctxTurns = append(ctxTurns, extractor.Turn{User: capText(s.transcript[i].User, 1200), Assistant: capText(s.transcript[i].Assistant, 1200)})
		}
	}
	glossary := s.glossary()
	s.mu.Unlock()

	props, err := s.prop.Propose(extractor.Turn{User: cur.User, Assistant: cur.Assistant}, ctxTurns, glossary)
	if err != nil {
		return // extractor failure degrades to plain transcript (invariant 6)
	}

	var ids []string
	for _, p := range props {
		kind := store.Kind(p.Kind)
		trust := store.TrustModelObserved
		if kind == store.KindDerived {
			trust = store.TrustModelDerived
		}
		// source attribution: the extractor PROPOSES source=user, but model
		// say-so must not mint VERIFIED facts (the keel lesson). Grant
		// OPERATOR_STATED only when the statement is deterministically
		// grounded in the user's own words.
		if p.Source == "user" && kind != store.KindDerived && groundedInText(p.Statement, cur.User) {
			trust = store.TrustOperatorStated
		}
		f := store.Fact{
			Statement:  p.Statement,
			Kind:       kind,
			Trust:      trust,
			Confidence: p.Confidence,
			Entities:   p.Entities,
			SessionID:  s.sessionID,
			Provenance: []store.Span{{SessionID: s.sessionID, Turn: turnN, Start: 0, End: len(cur.User) + len(cur.Assistant)}},
		}
		// reconcile-lite: exact-ish dup skip; overlap contradiction candidate
		if dupID, contraID := s.reconcileScan(p.Statement, p.Entities); dupID != "" {
			s.st.RecordRender(dupID, turnN) // re-observation confirms
			continue
		} else if contraID != "" { // asserted below, then linked+resolved
			if kind == store.KindDerived {
				// derived needs parents; contradiction candidates are weak parents — skip derived contras in v0
			}
			id, err := s.st.AssertFact(f)
			if err == nil {
				s.st.ResolveContradiction(id, contraID)
				ids = append(ids, id)
				s.saveEmbedding(id, p.Statement)
			}
			continue
		}
		if kind == store.KindDerived {
			// v0: parent = most recent fact sharing an entity; else demote to OBSERVED
			if pid := s.recentEntityFact(p.Entities); pid != "" {
				f.Parents = []string{pid}
			} else {
				f.Kind, f.Trust = store.KindObserved, store.TrustModelObserved
			}
		}
		if id, err := s.st.AssertFact(f); err == nil {
			ids = append(ids, id)
			s.saveEmbedding(id, p.Statement)
		}
	}
	s.mu.Lock()
	s.lastIDs = ids
	s.mu.Unlock()
}

// capText keeps the head and tail of oversized text (facts cluster at the
// edges of pasted-payload turns: the human sentence before, the ask after).
func capText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + "\n[…truncated…]\n" + s[len(s)-half:]
}

// groundedInText: at least 60% of the statement's content words appear in
// the candidate source text. Cheap, deterministic, and biased conservative —
// a false negative just leaves the fact at model trust/PROPOSED.
func groundedInText(statement, text string) bool {
	stmt := tokset(statement)
	src := tokset(text)
	if len(stmt) == 0 {
		return false
	}
	hit := 0
	for w := range stmt {
		if src[w] {
			hit++
		}
	}
	return float64(hit)/float64(len(stmt)) >= 0.6
}

func (s *Session) saveEmbedding(id, statement string) {
	if s.embedder == nil {
		return
	}
	if vec, err := s.embedder.Embed(statement); err == nil {
		s.st.SetEmbedding(id, vec)
	}
}

func tokset(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		out[w] = true
	}
	return out
}

func overlapF1(a, b string) float64 {
	A, B := tokset(a), tokset(b)
	if len(A) == 0 || len(B) == 0 {
		return 0
	}
	inter := 0
	for w := range A {
		if B[w] {
			inter++
		}
	}
	if inter == 0 {
		return 0
	}
	p, r := float64(inter)/float64(len(B)), float64(inter)/float64(len(A))
	return 2 * p * r / (p + r)
}

// Reconciler thresholds — calibrated on labeled pairs (embed/calibrate_test):
// UNREL topped out at 0.65; DUP spanned 0.88-0.94; CONTRA spanned 0.86-1.00.
// Cosine CANNOT separate dup from contradiction (a contradiction IS a
// near-paraphrase with one changed value — "caps at 40" vs "caps at 12"
// scored 0.996), so cosine only GATES same-topic candidates; the truth
// relation is judged by the 4B (PairJudge). Token-identity is the free
// fast path for verbatim dups.
const (
	sameTopicCos = 0.80 // below this: unrelated, no judgment needed
	tokenDupF1   = 0.90 // at/above this: duplicate without a model call
)

// reconcileScan returns (dupID, contraID) for a new statement against the
// store. Embedding+judge path when wired; token-overlap heuristics otherwise.
func (s *Session) reconcileScan(statement string, entities []string) (string, string) {
	if s.embedder == nil || s.judge == nil {
		return s.reconcileScanTokens(statement, entities)
	}
	vec, err := s.embedder.Embed(statement)
	if err != nil {
		return s.reconcileScanTokens(statement, entities)
	}
	all, err := s.st.Embeddings()
	if err != nil {
		return s.reconcileScanTokens(statement, entities)
	}
	// rank same-topic candidates by cosine, best first
	type cand struct {
		id  string
		cos float64
	}
	var cands []cand
	for id, v := range all {
		if c := cosine(vec, v); c >= sameTopicCos {
			cands = append(cands, cand{id, c})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].cos > cands[j].cos })
	if len(cands) > 3 {
		cands = cands[:3] // judge at most the 3 nearest — bounded model cost
	}
	for _, c := range cands {
		f, err := s.st.Get(c.id)
		if err != nil || f.Status == store.StatusRetracted {
			continue
		}
		if overlapF1(statement, f.Statement) >= tokenDupF1 {
			return f.ID, "" // verbatim dup, no model call
		}
		verdict, err := s.judge.JudgePair(statement, f.Statement)
		if err != nil {
			continue
		}
		switch verdict {
		case extractor.PairSame:
			return f.ID, ""
		case extractor.PairContradicts:
			return "", f.ID
		} // DISTINCT: keep scanning
	}
	return "", ""
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

// reconcileScanTokens is the pre-embedder fallback heuristic.
func (s *Session) reconcileScanTokens(statement string, entities []string) (string, string) {
	seen := map[string]bool{}
	var cands []*store.Fact
	for _, e := range entities {
		if fs, err := s.st.QueryEntity(e, 8); err == nil {
			for _, f := range fs {
				if !seen[f.ID] {
					seen[f.ID] = true
					cands = append(cands, f)
				}
			}
		}
	}
	for _, f := range cands {
		ov := overlapF1(statement, f.Statement)
		if ov >= 0.9 {
			return f.ID, ""
		}
		if ov >= 0.55 {
			return "", f.ID
		}
	}
	return "", ""
}

func (s *Session) recentEntityFact(entities []string) string {
	for _, e := range entities {
		if fs, err := s.st.QueryEntity(e, 1); err == nil && len(fs) > 0 {
			return fs[0].ID
		}
	}
	return ""
}

// glossary: entity slug → short description from its most recent fact.
func (s *Session) glossary() map[string]string {
	out := map[string]string{}
	core, err := s.st.Core()
	if err != nil {
		return out
	}
	for _, f := range core {
		for _, e := range f.Entities {
			if _, ok := out[e]; !ok {
				words := strings.Fields(f.Statement)
				if len(words) > 10 {
					words = words[:10]
				}
				out[e] = strings.Join(words, " ")
			}
		}
	}
	return out
}

// ---- transcript tail + persistence ----

func approxTokens(s string) int { return len(s)/4 + 1 }

// tailMessages returns the newest tail turns that fit both the TailTurns cap
// and the token budget, dropping oldest first.
func (s *Session) tailMessages(budget int) []backend.ProviderMessage {
	start := len(s.transcript) - s.cfg.TailTurns
	if start < 0 {
		start = 0
	}
	// walk backward from newest, accumulating within budget
	kept := 0
	spend := 0
	for i := len(s.transcript) - 1; i >= start; i-- {
		t := s.transcript[i]
		cost := approxTokens(t.User) + approxTokens(t.Assistant) + approxTokens(t.Injected)
		if spend+cost > budget {
			break
		}
		spend += cost
		kept++
	}
	var msgs []backend.ProviderMessage
	for _, t := range s.transcript[len(s.transcript)-kept:] {
		if t.Injected != "" {
			msgs = append(msgs, backend.ProviderMessage{Role: "user", Content: t.Injected})
		}
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
