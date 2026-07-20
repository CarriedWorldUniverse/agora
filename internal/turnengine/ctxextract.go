package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/extractor"
)

// extractTimeout bounds a single extraction model call. Extraction runs on the
// ctxmap engine's SINGLE serial worker, and Manager.Run's teardown blocks on
// eng.Close() → that worker draining — so a call that never returns (dead
// gateway, a stream that never closes) would wedge the worker AND hang shutdown
// forever. context.Background() is detached from the turn lifecycle, so this
// timeout is the only cancellation on this path. Generous: a reasoner model's
// extraction pass can legitimately take tens of seconds.
const extractTimeout = 120 * time.Second

// activeModelExtractor implements ctxmap's Proposer + PairJudge seams using the
// harness's OWN active provider/model — no separate extraction tier, no llama.cpp
// side-stack, no gateway-specific config. Whatever /model is set to (kimi, glm,
// sonnet, …) is what distills that model's own conversation into durable facts.
// This is the "the configured model should just use it" wiring.
//
// Honest scope note: with agora's nil Embedder the engine's reconcileScan
// short-circuits to token-overlap matching and NEVER reaches the PairJudge
// (ctxmap/memory/engine.go: `if e.emb == nil || e.judge == nil` → token path).
// JudgePair below is therefore DORMANT in production today — implemented and
// tested because it's the seam contract, and it goes live the moment an
// Embedder is wired, but reconciliation currently runs on token overlap alone.
//
// active() returns the current (provider, model). The engine runs extraction on
// a background worker, so active() is called off the turn goroutine — the
// Manager guards the fields it reads (see setActiveModel).
type activeModelExtractor struct {
	active func() (bridle.Provider, string)
}

// discardSink drops all stream events: extraction only needs the final text
// (ProviderResult.FinalText), not the live deltas.
type discardSink struct{}

func (discardSink) Emit(bridle.Event) {}

// complete makes ONE tool-free, hook-free model call for extraction. It calls
// the provider's RunTurn DIRECTLY rather than through the harness, so an
// extraction pass never re-enters the approval / ctxmap / tool hooks (no
// recursion, no spurious approval prompts) and never surfaces as an agora turn.
func (x *activeModelExtractor) complete(sys, user string) (string, error) {
	prov, model := x.active()
	if prov == nil || model == "" {
		return "", fmt.Errorf("ctxextract: no active provider/model")
	}
	// Only extract on DIRECT-API providers (the local-model path): a one-shot
	// RunTurn there is a plain HTTP call. A subprocess provider (claudesdk) would
	// spawn its Node sidecar per extraction — expensive, and it already carries
	// server-side session memory, so it needs ctxmap extraction least. Skip it
	// (a returned error degrades that turn to plain-transcript, no facts).
	if prov.Capabilities().Category != bridle.CategoryDirectAPI {
		return "", fmt.Errorf("ctxextract: skipped, provider %q is not direct-api", prov.Name())
	}
	ctx, cancel := context.WithTimeout(context.Background(), extractTimeout)
	defer cancel()
	res, err := prov.RunTurn(ctx, bridle.ProviderRequest{
		AppendSystemPrompt: sys,
		Model:              model,
		Messages:           []bridle.ProviderMessage{{Role: "user", Content: user}},
	}, discardSink{})
	if err != nil {
		return "", err
	}
	return res.FinalText, nil
}

// Propose extracts durable facts from the current turn (ctxmap Proposer seam).
// Failures are logged to stderr (→ agora's log file in TUI mode) BEFORE being
// returned: the ctxmap engine deliberately swallows Propose errors (extraction
// degrades to plain transcript), so without this line a broken extraction path
// is fully silent — which is exactly how the first live-run failure hid.
func (x *activeModelExtractor) Propose(current extractor.Turn, ctxTurns []extractor.Turn, glossary map[string]string) ([]extractor.FactProposal, error) {
	out, err := x.complete(extractSystemPrompt, buildExtractUser(current, ctxTurns, glossary))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxextract: propose failed: %v\n", err)
		return nil, err
	}
	facts, perr := parseFacts(out)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "ctxextract: propose parse failed: %v\n", perr)
		return nil, perr
	}
	fmt.Fprintf(os.Stderr, "ctxextract: proposed %d fact(s)\n", len(facts))
	return facts, nil
}

// JudgePair classifies two same-topic statements (ctxmap PairJudge seam).
func (x *activeModelExtractor) JudgePair(a, b string) (extractor.PairVerdict, error) {
	out, err := x.complete(pairSystemPrompt, "A: "+a+"\nB: "+b+"\n\nVerdict?")
	if err != nil {
		return "", err
	}
	return parseVerdict(out)
}

// buildExtractUser renders the user half of the extraction prompt: known
// entities (so slugs are reused, not reinvented), prior turns for pronoun
// resolution only, then the current turn to extract from. Mirrors the llama
// extractor's buildExtractionPrompt but leaves role framing to the provider
// (no ChatML markers — the openai/claude providers add their own).
func buildExtractUser(current extractor.Turn, ctxTurns []extractor.Turn, glossary map[string]string) string {
	var b strings.Builder
	if len(glossary) > 0 {
		b.WriteString("KNOWN ENTITIES:\n")
		keys := make([]string, 0, len(glossary))
		for k := range glossary {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %s\n", k, glossary[k])
		}
		b.WriteString("\n")
	}
	if len(ctxTurns) > 0 {
		b.WriteString("PREVIOUS TURNS (context only, do not re-extract):\n")
		for _, t := range ctxTurns {
			fmt.Fprintf(&b, "[user]: %s\n[assistant]: %s\n", t.User, t.Assistant)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "CURRENT TURN (extract from this):\n[user]: %s\n[assistant]: %s", current.User, current.Assistant)
	return b.String()
}

// parseFacts pulls the JSON fact array out of a model reply. Robust to thinking
// preambles (cut after the last </think>), ```json fences, and prose that itself
// contains stray brackets ("see step [2]", "[Understood]…"): it tries EACH '['
// as a candidate array start, balanced-matches its ']' (string-literal aware),
// and returns the first slice that actually parses as a fact array — so a stray
// bracket in prose can't extend/shift the span and lose a well-formed batch. An
// empty / "no facts" reply is a valid zero result, not an error.
func parseFacts(out string) ([]extractor.FactProposal, error) {
	s := stripThink(out)
	for i := 0; i < len(s); i++ {
		if s[i] != '[' {
			continue
		}
		end := matchBracket(s, i)
		if end < 0 {
			continue
		}
		var facts []extractor.FactProposal
		if json.Unmarshal([]byte(s[i:end+1]), &facts) == nil {
			return facts, nil
		}
	}
	// No parseable array anywhere — treat as "nothing durable this turn" rather
	// than a hard error, so a terse/no-facts reply never spams the worker.
	return nil, nil
}

// matchBracket returns the index of the ']' that balances the '[' at start,
// tracking nesting and skipping brackets inside JSON string literals (honoring
// backslash escapes). Returns -1 if unbalanced.
func matchBracket(s string, start int) int {
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseVerdict extracts the pair verdict. The prompt asks the model to "think
// briefly, then answer" — the real answer is the LAST verdict word in the reply,
// not the first by priority. Scanning first-match-by-priority (the llama judge's
// approach, which relied on a terse post-</think> reply) mis-reads a reasoned
// "not DISTINCT, nor CONTRADICTS — verdict: SAME" as CONTRADICTS, feeding a
// WRONG verdict into reconciliation. So take the latest-occurring candidate.
func parseVerdict(out string) (extractor.PairVerdict, error) {
	v := stripThink(out)
	best := -1
	var found extractor.PairVerdict
	for _, cand := range []extractor.PairVerdict{extractor.PairSame, extractor.PairContradicts, extractor.PairDistinct} {
		if idx := strings.LastIndex(v, string(cand)); idx > best {
			best, found = idx, cand
		}
	}
	if best < 0 {
		return "", fmt.Errorf("ctxextract: no pair verdict in %q", strings.TrimSpace(out))
	}
	return found, nil
}

// stripThink drops everything up to and including a closing </think> tag, so a
// reasoning model whose thoughts land in the content (rather than a separate
// reasoning_content stream) doesn't poison JSON/verdict parsing.
func stripThink(s string) string {
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		return s[i+len("</think>"):]
	}
	return s
}

// The extraction/judgment prompts are ported verbatim from bridle's llama
// extractor (ctxmap/extractor/llama_extractor.go) so the active-model path
// yields the same fact shape and reconciliation semantics as the in-process
// model path — only the executor differs.
const extractSystemPrompt = `You extract durable facts from conversation turns for a working-memory store. Output ONLY a JSON array of fact objects: {"statement","kind","source","entities","confidence"}.

SOURCE — who ASSERTED the fact:
- "user": the fact's substance was stated by the user (decisions, corrections, orders, reports). If the assistant merely restates or confirms what the user said, the source is still "user".
- "assistant": the fact's substance was introduced by the assistant (its own observations, conclusions, plans).

WHAT COUNTS AS ONE FACT:
- One real-world fact = ONE entry: merge clauses about the SAME thing into one complete statement; decisions about DIFFERENT things are separate entries. Never split one fact, never fuse two.
- When the assistant merely restates, confirms, or acknowledges what the user said, that is the SAME fact — extract it once, not twice.
- General knowledge explanations (how something works in general) are NOT durable session facts. A turn that is question + textbook answer => [].
- QUESTIONS assert nothing: extract NO facts from a question — including facts the question presupposes ("why does the broker on li1 drop connections?" does NOT establish that the broker is on li1).
- For a REQUEST or ORDER ("make X do Y", "please change Z"), the fact is the operator's INTENT — phrase it "The operator wants …" — never as accomplished state.
- Transient chit-chat, greetings, scheduling small-talk => [].

KIND rubric — the DEFAULT is OBSERVED:
- OBSERVED: state, events, decisions, corrections, descriptions. When unsure, use OBSERVED.
- CONSTRAINT: ONLY a standing rule about how things MUST or MUST NEVER be done, phrased as law ("must never allocate", "no structure steeper than 30 degrees", "nothing below q8"). A decision or state is NOT a constraint.
- PREFERENCE: ONLY how the operator personally likes things done ("I prefer", "from now on do X", style/format/habit).
- DERIVED: ONLY a new conclusion or diagnosis reasoned out in this turn ("so the cause is X", "therefore I'll do Y"), not something directly stated.

Rules:
- statement: one short self-contained declarative sentence; resolve all pronouns using context.
- entities: kebab-case slugs. If a KNOWN ENTITIES slug applies, use it VERBATIM. Never invent spaced or capitalized names.
- Do not re-extract facts from PREVIOUS TURNS; they are context for pronoun resolution only.`

const pairSystemPrompt = `Two statements about the same topic. Think briefly, then answer with exactly one word:
- SAME: they assert the same fact (different wording is fine)
- CONTRADICTS: they cannot both be true (a value, place, or polarity differs) — OR one is a standing rule/constraint and the other asks or intends to violate it
- DISTINCT: compatible but different facts about the topic`
