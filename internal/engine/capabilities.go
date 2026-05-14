// Worker provider capability matrix — NEX-71.
//
// agora's controller/worker dispatch (NEX-70) needs the planner to
// know which providers are valid workers and what each can do, so
// it can decompose tasks without asking for capabilities the worker
// can't deliver. The matrix here is the source of truth: it drives
// the dispatch_subtask tool schema's `provider` enum, gets rendered
// into the planner's system prompt (NEX-72), and is queried by the
// BeforeToolCall hook to validate dispatch arguments before spawning
// a worker turn.
//
// Important: this matrix lists **worker** providers only. Planner-
// class providers (claudecode, claude-pty, claude-api running Opus)
// are explicitly excluded — depth=1 invariant from chat #975/#976
// means workers don't dispatch, so listing them as workers would
// either be moot (claude-pty is for planners) or a cost trap
// (claude-api Opus as a worker = expensive metered Opus burn that
// erases the cost win).
//
// To add a worker provider: append to WorkerProviders. The dispatch
// tool schema, prompt injection, and validation all derive from
// this slice — no other call sites to update.
package engine

import (
	"fmt"
	"sort"
	"strings"

	bridle "github.com/CarriedWorldUniverse/bridle"
)

// LatencyTier is a coarse hint to the planner about worker speed.
// Affects decomposition choice for time-sensitive vs background work.
type LatencyTier string

const (
	LatencyFast   LatencyTier = "fast"   // sub-3s typical turn
	LatencyMedium LatencyTier = "medium" // 3-10s typical
	LatencySlow   LatencyTier = "slow"   // 10s+ typical (e.g. DeepSeek deep-thinking)
)

// CostTier is a coarse hint to the planner about per-turn cost
// relative to Opus. cheap = pennies; moderate = single-digit cents;
// expensive = approaching Opus. Used in prompt-side guidance about
// when dispatching is worth the cost.
type CostTier string

const (
	CostCheap     CostTier = "cheap"     // ~1/20 Opus or better
	CostModerate  CostTier = "moderate"  // ~1/5 Opus
	CostExpensive CostTier = "expensive" // approaching Opus
)

// WorkerCapability describes one provider's worker-side surface.
// Fields are stable; planner reads these via the prompt injection,
// dispatch validation reads them at call time.
type WorkerCapability struct {
	// ID is bridle.ProviderID — what gets passed in TurnRequest.Provider.
	ID bridle.ProviderID

	// DefaultModel is the model used when dispatch_subtask omits the
	// `model` arg. Provider-specific id (e.g. "gpt-5-codex",
	// "deepseek-chat").
	DefaultModel string

	// SupportsCustomTools mirrors ProviderCapabilities.SupportsCustomTools
	// — whether the worker can be given agora-defined ToolDefs.
	// claudecode is false (own agent loop); openai/claude-api are true.
	SupportsCustomTools bool

	// SupportsMCP mirrors ProviderCapabilities.SupportsMCP — whether
	// the worker can load MCP servers from cwd's .mcp.json.
	SupportsMCP bool

	// Latency is the planner-visible speed tier.
	Latency LatencyTier

	// Cost is the planner-visible cost tier.
	Cost CostTier

	// Notes is free-form caveats / strengths the planner should know
	// (e.g. "strong on code, weaker on long context", "rate-limited
	// at $X/min during peak hours"). Appears in the prompt block.
	Notes string
}

// WorkerProviders is the canonical v1 list. Order matters: the
// dispatch_subtask tool schema's `provider` enum is built in this
// order so the first entry is the "natural default" if the planner
// doesn't specify a strong preference.
//
// DeepSeek first (cheapest, default execution lane), OpenAI second
// (fast-tier for latency-sensitive work). Claude-anything is
// deliberately absent (depth=1 + cost-leak risk).
var WorkerProviders = []WorkerCapability{
	{
		ID:                  bridle.ProviderID("deepseek-api"),
		DefaultModel:        "deepseek-chat",
		SupportsCustomTools: true,
		SupportsMCP:         true,
		Latency:             LatencySlow,
		Cost:                CostCheap,
		Notes:               "~1/30 Opus cost. V3.1 solid on code; thoughtful turns can take 8-30s. Default worker for non-latency-bound work.",
	},
	{
		ID:                  bridle.ProviderOpenAI,
		DefaultModel:        "gpt-5-codex",
		SupportsCustomTools: true,
		SupportsMCP:         true,
		Latency:             LatencyFast,
		Cost:                CostModerate,
		Notes:               "Fast (<3s typical). Strong on code + spec adherence. ~1/5 Opus cost. Use when latency matters or DeepSeek is rate-limited.",
	},
}

// Find returns the capability for the given provider id, plus a
// bool indicating whether it was in the worker matrix at all. Use
// this in the BeforeToolCall hook to reject dispatches against
// non-worker providers (claudecode, claude-pty, claude-api).
func Find(id bridle.ProviderID) (WorkerCapability, bool) {
	for _, w := range WorkerProviders {
		if w.ID == id {
			return w, true
		}
	}
	return WorkerCapability{}, false
}

// ProviderIDs returns the set of valid worker provider ids in
// declaration order. Drives the dispatch_subtask tool schema's
// `provider` enum and prompt-side rendering.
func ProviderIDs() []bridle.ProviderID {
	out := make([]bridle.ProviderID, 0, len(WorkerProviders))
	for _, w := range WorkerProviders {
		out = append(out, w.ID)
	}
	return out
}

// ValidateDispatch checks a (provider, useTools, useMCP) tuple
// against the matrix. Returns nil if the request is satisfiable;
// otherwise a human-readable reason the planner can recover from.
//
// Designed so the BeforeToolCall hook can call this BEFORE spawning
// any worker turn — bad combinations get rejected synchronously
// (hard error per anvil #975) rather than burning a worker call
// that errors at the provider boundary.
func ValidateDispatch(provider bridle.ProviderID, useTools, useMCP bool) error {
	cap, ok := Find(provider)
	if !ok {
		valid := make([]string, 0, len(WorkerProviders))
		for _, w := range WorkerProviders {
			valid = append(valid, string(w.ID))
		}
		sort.Strings(valid)
		return fmt.Errorf("provider %q is not a valid worker (valid: %s)",
			provider, strings.Join(valid, ", "))
	}
	if useTools && !cap.SupportsCustomTools {
		return fmt.Errorf("worker provider %q does not support custom tools", provider)
	}
	if useMCP && !cap.SupportsMCP {
		return fmt.Errorf("worker provider %q does not support MCP", provider)
	}
	return nil
}

// RenderMatrixPrompt returns a markdown block describing the worker
// matrix in a form suitable for inclusion in the planner's system
// prompt (NEX-72 wires it in). Table format with the columns the
// planner needs to make decomposition tradeoffs: provider, model,
// latency, cost, tools? mcp? notes.
func RenderMatrixPrompt() string {
	var b strings.Builder
	b.WriteString("## Available worker providers\n\n")
	b.WriteString("When you call `dispatch_subtask`, pick the worker that fits the task. ")
	b.WriteString("Trade off cost vs latency vs capability per the table below.\n\n")
	b.WriteString("| provider | default model | latency | cost | custom tools? | MCP? | notes |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	for _, w := range WorkerProviders {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | %s | %s |\n",
			w.ID, w.DefaultModel, w.Latency, w.Cost,
			yesNo(w.SupportsCustomTools), yesNo(w.SupportsMCP), w.Notes)
	}
	return b.String()
}
