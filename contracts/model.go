package contracts

import (
	"context"
	"encoding/json"
)

// Effort is the reasoning-effort ladder (the real Claude surface); bridle maps
// per provider and drops unsupported tiers with a once-per-session warning.
// agora default = high; xhigh/max are opt-in, never the default.
// Spec: agora-spec-bridle.md §3 (effort translation).
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// SystemPromptMode: whether a lane owns the whole system prompt or only an
// append slot (claude-code CLI). The prompt compose branches structurally.
// Spec: agora-spec-bridle.md §1, agora-spec-prompt.md §4.
type SystemPromptMode string

const (
	SystemPromptFull   SystemPromptMode = "full"
	SystemPromptAppend SystemPromptMode = "append"
)

// Capabilities advertises what a model/lane can do.
// Spec: agora-spec-bridle.md §1.
type Capabilities struct {
	Tools            bool             `json:"tools"`
	ParallelTools    bool             `json:"parallel_tools"`
	Streaming        bool             `json:"streaming"`
	ReasoningEffort  bool             `json:"reasoning_effort"`
	StructuredOutput bool             `json:"structured_output"`
	PromptCaching    bool             `json:"prompt_caching"`
	Vision           bool             `json:"vision"`
	SystemPromptMode SystemPromptMode `json:"system_prompt_mode"`
}

// Pricing enables cost-aware workflow budgets (token-only until present).
type Pricing struct {
	In     float64 `json:"in"`
	Out    float64 `json:"out"`
	Cached float64 `json:"cached,omitempty"`
}

// PromptMeta is the MODEL-GLOBAL prompt-presentation metadata; per-core
// adjustments and renditions live in the core package, not here.
// Spec: agora-spec-prompt.md §4, agora-spec-bridle.md §1.
type PromptMeta struct {
	// Dialect knobs: presentation-only (tool idiom, format, thinking
	// guidance). A dialect may rephrase/reformat, never add/remove contract.
	Dialect map[string]string `json:"dialect,omitempty"`
	// RenditionRef points at a compiled per-model rendition of a core (in the
	// core package, keyed by core hash) when adopted.
	RenditionRef string `json:"rendition_ref,omitempty"`
}

// ModelInfo is the registry entry agora requires of bridle.
// Spec: agora-spec-bridle.md §1.
type ModelInfo struct {
	ID              string       `json:"id"`
	Aliases         []string     `json:"aliases,omitempty"`
	ContextWindow   int64        `json:"context_window"`
	MaxOutputTokens int64        `json:"max_output_tokens"`
	Capabilities    Capabilities `json:"capabilities"`
	Pricing         *Pricing     `json:"pricing,omitempty"`
	Prompt          *PromptMeta  `json:"prompt,omitempty"`
}

// ErrorClass is bridle's normalized error taxonomy; agora retry policy keys
// off it (rate_limit/overloaded/network retry with backoff; auth surfaces;
// context_length routes to the ContextManager; refusal is non-retryable).
// Spec: agora-spec-bridle.md §3.
type ErrorClass string

const (
	ErrAuth          ErrorClass = "auth"
	ErrRateLimit     ErrorClass = "rate_limit"
	ErrOverloaded    ErrorClass = "overloaded"
	ErrContextLength ErrorClass = "context_length"
	ErrSchema        ErrorClass = "schema"
	ErrNetwork       ErrorClass = "network"
	ErrRefusal       ErrorClass = "refusal"
	ErrProvider      ErrorClass = "provider"
)

// StopReason terminates a stream's done event.
// Spec: agora-spec-bridle.md §2.
type StopReason string

const (
	StopEnd       StopReason = "end"
	StopToolCalls StopReason = "tool_calls"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
)

// StreamEventType tags normalized provider-stream events — agora never sees
// provider wire formats.
// Spec: agora-spec-bridle.md §2.
type StreamEventType string

const (
	StreamTextDelta      StreamEventType = "text_delta"
	StreamReasoningDelta StreamEventType = "reasoning_delta"
	StreamToolCall       StreamEventType = "tool_call"
	StreamUsage          StreamEventType = "usage"
	StreamDone           StreamEventType = "done"
	StreamError          StreamEventType = "error"
	StreamWarning        StreamEventType = "warning"
)

// StreamEvent is one normalized event from a model stream.
type StreamEvent struct {
	Type StreamEventType `json:"type"`
	// S carries delta text for text_delta/reasoning_delta.
	S string `json:"s,omitempty"`
	// ToolCall is complete (bridle assembles streamed arg fragments).
	ToolCall *ToolCall  `json:"tool_call,omitempty"`
	Usage    *Usage     `json:"usage,omitempty"`
	Stop     StopReason `json:"stop_reason,omitempty"`
	Error    ErrorClass `json:"error,omitempty"`
	Message  string     `json:"message,omitempty"`
}

// ToolCall is a completed model tool invocation.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args_json"`
}

// Request is what agora hands bridle per sampling request. bridle sees only
// final messages + tools — it stays a model funnel, not a harness.
// Spec: agora-spec-bridle.md §2, §4.
type Request struct {
	Messages  []AssembledMessage `json:"messages"`
	Tools     []ToolSpec         `json:"tools,omitempty"`
	Effort    Effort             `json:"effort,omitempty"`
	MaxTokens int64              `json:"max_tokens,omitempty"`
	// Structured forces schema-validated output (native mode or single-tool
	// forcing; result validates or error{schema}).
	Structured json.RawMessage `json:"structured,omitempty"`
	// CacheHints marks the stable prefix boundary (index of the last
	// cache-stable message).
	CacheHints int `json:"cache_hints,omitempty"`
}

// ModelStreamer is the turn-execution seam agora requires of bridle.
// Cancellation via ctx must abort the upstream request promptly.
// Spec: agora-spec-bridle.md §2.
type ModelStreamer interface {
	Resolve(aliasOrID string, identity Identity) (ModelInfo, error)
	List() ([]ModelInfo, error)
	Stream(ctx context.Context, model ModelInfo, req Request) (<-chan StreamEvent, error)
}
