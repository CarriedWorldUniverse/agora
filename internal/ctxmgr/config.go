package ctxmgr

// Config is the §6 [context] profile block. Defaults below are the
// testing-derived defaults from the spec — TUNING (choosing different
// values for a specific deployment) is an explicit interactive follow-on
// task (agora-spec-build.md §0.5); this unit's job is to make every knob
// an explicit, documented parameter and prove the ALGORITHM against them,
// not to justify the numbers themselves.
type Config struct {
	// WsetBudgetFrac: resident-layer budget, as a fraction of
	// ModelInfo.ContextWindow. Default 0.25 (§3a).
	WsetBudgetFrac float64
	// KeepOthers: recent non-keyed (unkeyed/command) tool results kept
	// verbatim in tier 4. Default 2 (§1 tier 4, §6).
	KeepOthers int
	// MaxRetainBytes: per-item cap applied before the budget — any resident
	// item over this is head-truncated with an idempotent marker. Default
	// 65536 (§3a).
	MaxRetainBytes int
	// HotSteps: keys touched within the last HotSteps steps are immune to
	// demotion regardless of size. Default 3 (§3a).
	HotSteps int
	// EvictTo: hysteresis floor — an eviction episode (triggered at 100% of
	// budget) demotes down to this fraction of budget. Default 0.70 (§3a).
	EvictTo float64
	// TrackedMaxKeys: LRU cap on the tracked layer (metadata only, bounds
	// sweep cost). Default 1024 (§3b).
	TrackedMaxKeys int
	// ReadmitOnMention: anticipatory re-admission when a drift/diagnostic
	// report or command output mentions a tracked key (§3b trigger 2).
	// Default true (§6).
	ReadmitOnMention bool
	// PartialThreshold: files below this size always re-admit whole, never
	// partial-by-span (§3b). Default 4096.
	PartialThreshold int
	// ReasoningKeepTurns: reasoning/thinking blocks are dropped beyond this
	// many turns where the provider allows it (§4). Default 2. Not enforced
	// by ctxmgr directly (bridle's per-provider replay contract owns
	// reasoning-block passthrough, §4) — carried here only so the knob is
	// visible in one config surface, per the spec's single [context] table.
	ReasoningKeepTurns int
	// DialogueKeepTurns: last-resort dialogue summarization threshold (§5).
	// Default 8. Summarization itself is out of this unit's scope (Compact
	// wires the trigger; the summarizer is the turn engine's alias call).
	DialogueKeepTurns int
	// SpanIndexerName records which SpanIndexer the profile selected ("" =
	// line-window fallback, "codemap" = the deferred real indexer per §3b —
	// not implemented by this unit, see spanindex.go). Default "".
	SpanIndexerName string
	// Keys maps tool name -> class/key-arg (§6 [context.keys]).
	Keys map[string]KeyMapping
}

// DefaultConfig returns the §6 documented defaults.
func DefaultConfig() Config {
	return Config{
		WsetBudgetFrac:     0.25,
		KeepOthers:         2,
		MaxRetainBytes:     65536,
		HotSteps:           3,
		EvictTo:            0.70,
		TrackedMaxKeys:     1024,
		ReadmitOnMention:   true,
		PartialThreshold:   4096,
		ReasoningKeepTurns: 2,
		DialogueKeepTurns:  8,
		SpanIndexerName:    "",
		Keys: map[string]KeyMapping{
			"read_file":   {Domain: "file", Class: ClassRead, KeyArg: "path"},
			"write_file":  {Domain: "file", Class: ClassWrite, KeyArg: "path"},
			"apply_patch": {Domain: "file", Class: ClassEdit, KeyArg: "path"},
		},
	}
}

// BudgetBytes computes the resident-layer byte budget from a model's
// context window (in tokens), using the §3a chars/4 estimator. Returns -1
// ("no budget configured, don't run eviction episodes") when the window is
// unknown — distinct from a genuine zero budget, which would evict
// everything.
func (c Config) BudgetBytes(contextWindowTokens int64) int64 {
	if contextWindowTokens <= 0 {
		return -1
	}
	tokens := float64(contextWindowTokens) * c.WsetBudgetFrac
	return int64(tokens) * BytesPerToken
}

// BytesPerToken is the §3a "chars/4" estimator's ratio.
const BytesPerToken = 4
