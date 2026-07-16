package contracts

// CompactionTrigger says why a compaction episode ran.
// Spec: agora-spec-context.md §1.
type CompactionTrigger string

const (
	CompactManual CompactionTrigger = "manual"
	CompactAuto   CompactionTrigger = "auto"
)

// CompactionResult reports a compaction episode for the wire events.
// Spec: agora-spec-context.md §2 contract #4.
type CompactionResult struct {
	Trigger      CompactionTrigger `json:"trigger"`
	TokensBefore int64             `json:"tokens_before"`
	TokensAfter  int64             `json:"tokens_after"`
	// NoOp: managers that curate continuously may have nothing to do.
	NoOp bool `json:"no_op,omitempty"`
}

// AssembledMessage is one model-visible message produced by Assemble.
// Role is agora's abstract role (prompt §1a); bridle translates per provider.
type AssembledMessage struct {
	Role Role `json:"role"`
	// Content is the rendered text (tool blocks etc. are the funnel's
	// concern at the bridle Request layer; the seam here is message-shaped).
	Content string `json:"content"`
	// CacheStable marks the prefix segment for cache_hints (bridle §3).
	CacheStable bool `json:"cache_stable,omitempty"`
}

// ContextManager is the context-management seam. The curation algorithm
// (agora-spec-context-curation.md) plugs in behind it; a trivial
// assemble-verbatim implementation is a valid v0.
//
// Fixed contracts that hold for ANY implementation (agora-spec-context.md §2):
//  1. persisted thread items are never rewritten — assembly is a projection;
//  2. Pre/PostCompact hooks fire around summarization episodes;
//  3. state fragments (tool schemas, skills catalog, AGENTS.md, memory index,
//     identity/mode) are regenerated fresh, never summarized;
//  4. wire events emitted (compaction pair; curation events for view-only LRU);
//  5. never mid-turn — episodes run between sampling requests;
//  6. workflow journal / agent graph are not model context, never compact;
//  7. context_length errors route here for one compact-and-retry;
//  8. /status reads the manager's numbers.
type ContextManager interface {
	// Assemble produces the model-visible context for a sampling request,
	// within ModelInfo.ContextWindow.
	Assemble(threadID string, turnInput []ThreadItem) ([]AssembledMessage, error)
	// Observe is fed after every request (bridle usage event).
	Observe(u Usage)
	// Compact runs an episode (LRU first, dialogue summarization last resort).
	Compact(trigger CompactionTrigger) (CompactionResult, error)
	// Status exposes effective window, current estimate, percent remaining.
	Status() ContextStatus
}

// ContextStatus backs /status and the TUI footer percentage.
// Spec: agora-spec-context.md §2 contract #8.
type ContextStatus struct {
	EffectiveWindow  int64   `json:"effective_window"`
	CurrentEstimate  int64   `json:"current_estimate"`
	PercentRemaining float64 `json:"percent_remaining"`
}
