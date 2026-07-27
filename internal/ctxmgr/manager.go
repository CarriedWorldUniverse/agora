package ctxmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// DiskReader is the seam Manager uses for §3b re-admission source 2
// ("stale but disk-backed" — "the HARNESS reads the disk itself"). The
// real implementation is a sandboxed file read (mcp/fs family territory);
// ctxmgr only needs the narrow read+hash contract.
type DiskReader interface {
	ReadFile(path string) (data []byte, hash string, err error)
}

// StateFragments is the tier-1 seam: system prompt, tool schemas, skills
// catalog, AGENTS.md, MEMORY.md index, identity/mode — all "regenerated
// fresh every assembly" (context spec §2 contract 3). Those fragments are
// built by other packages (prompt/skills/mcp/memory); this unit only
// defines where they slot into the assembly. A nil StateFragments field
// on Manager means no fragments are prepended (a valid, if degenerate,
// v1 — matches the ContextManager doc comment's "trivial assemble-verbatim
// implementation is a valid v0" for tier 1 specifically).
type StateFragments func() []contracts.AssembledMessage

// Manager implements contracts.ContextManager per the curation algorithm.
// One Manager per thread (per the context spec's "Per-thread
// ContextManager"). Per §8, the ledger is rebuilt fresh from turnInput on
// every Assemble call — nothing about the working set is persisted across
// calls; the only state that DOES persist across calls is external truth
// this unit cannot re-derive from the thread: fs-watcher observations
// (ApplyFSChange) and the token-estimate correction factor (Observe).
type Manager struct {
	cfg         Config
	model       contracts.ModelInfo
	clock       Clock
	hooks       HookRunner
	diskReader  DiskReader
	spanIndexer SpanIndexer
	fragments   StateFragments

	estimator *Estimator

	// mu guards the mutable per-thread state below (fsObserved, estimator,
	// lastEstimate/lastRawChars/lastEvents). ApplyFSChange is called by the
	// async fs-watcher seam (mcp.Sweeper/notify) which can race a concurrent
	// Assemble — an unguarded fsObserved map read+write is a fatal Go panic.
	mu sync.Mutex

	// fsObserved: latest known on-disk (hash, kind) per path, fed by
	// ApplyFSChange — the fs-watcher signal (mcp §5a / Sweeper) applied at
	// the next Assemble's ledger build.
	fsObserved map[string]contracts.FSChange

	lastEstimate int64
	lastRawChars int
	lastEvents   []contracts.Event
}

// NewManager builds a Manager for one thread against model. hooks/
// diskReader/spanIndexer/fragments/clock may all be nil — nil hooks uses
// NoopHookRunner, nil clock uses SystemClock, nil spanIndexer uses the
// line-window fallback, nil fragments prepends nothing, nil diskReader
// means ReadmitNeedsDiskRead re-admissions fail closed (ErrNoDiskReader)
// rather than silently serving stale content.
func NewManager(cfg Config, model contracts.ModelInfo, opts ...Option) *Manager {
	m := &Manager{
		cfg:        cfg,
		model:      model,
		clock:      SystemClock{},
		hooks:      NoopHookRunner{},
		estimator:  NewEstimator(),
		fsObserved: make(map[string]contracts.FSChange),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Option configures a Manager at construction.
type Option func(*Manager)

func WithClock(c Clock) Option                   { return func(m *Manager) { m.clock = c } }
func WithHookRunner(h HookRunner) Option         { return func(m *Manager) { m.hooks = h } }
func WithDiskReader(d DiskReader) Option         { return func(m *Manager) { m.diskReader = d } }
func WithSpanIndexer(s SpanIndexer) Option       { return func(m *Manager) { m.spanIndexer = s } }
func WithStateFragments(f StateFragments) Option { return func(m *Manager) { m.fragments = f } }

// ApplyFSChange feeds one fs-watcher observation (mcp.Sweeper/notify,
// contracts.FSChange) into the manager, consumed at the next Assemble.
// Per contract #5, this is called by the turn engine BETWEEN sampling
// requests, never mid-turn.
func (m *Manager) ApplyFSChange(c contracts.FSChange) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fsObserved[c.Path] = c
}

// pendingRead tracks a ClassRead tool_call awaiting its result.
type pendingRead struct {
	key        Key
	diskBacked bool
}

// Assemble implements contracts.ContextManager. Interpretation note (the
// spec fixes the signature, not the semantics of turnInput): turnInput is
// read as the full ordered set of ThreadItems to render for this request
// (the thread's items up to and including the turn being assembled) —
// consistent with §8 ("ledger rebuildable by thread replay: persist
// nothing in v1") and with Assemble needing to see every prior keyed
// artifact to decide what's superseded. The four §1 tiers are realized as:
// tier 1 (state) is PREPENDED fresh; tiers 2-4 are rendered in the
// thread's ORIGINAL order with per-item treatment (verbatim / working-set
// live-or-stub / recent-window cap) rather than physically regrouped —
// this is what keeps the assembly prefix-stable (rule 5) and matches §3b
// "the stub sits in a tool result" (at ITS position, not moved to a
// separate block).
//
// Eviction is likewise collapsed to one episode per Assemble call over the
// final replayed state (not one discrete episode per historical trigger
// point) — Assemble is a stateless projection of "the current view", so
// only the final steady state matters; readmission-by-mention (§3b trigger
// 2) is applied before that episode so a late mention can save a key from
// demotion in the same pass it would otherwise have been evicted.
func (m *Manager) Assemble(threadID string, turnInput []contracts.ThreadItem) ([]contracts.AssembledMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastEvents = nil
	ledger := NewLedger(m.cfg)
	pending := make(map[string]pendingRead)

	// Pass 1: build the ledger from tool calls/results.
	for i, item := range turnInput {
		step := i
		switch item.Type {
		case contracts.TIToolCall:
			var tc ToolCallPayload
			if !decodePayload(item.Payload, &tc) {
				continue
			}
			mapping, ok := m.cfg.Keys[tc.ToolName]
			if !ok {
				continue // unkeyed (command) — tier 4, no ledger entry
			}
			id := argStringAny(tc.Args, mapping.KeyArg)
			if id == "" {
				continue
			}
			k := Key{Domain: mapping.Domain, ID: id}
			diskBacked := mapping.Domain == "file"
			switch mapping.Class {
			case ClassWrite:
				content := argString(tc.Args, "content")
				ledger.RecordWrite(step, k, item.Seq, hashBytes([]byte(content)), len(content), diskBacked)
			case ClassEdit:
				ledger.RecordMutation(step, k, diskBacked)
			case ClassRead:
				ledger.Touch(step, k)
				if tc.ID != "" {
					pending[tc.ID] = pendingRead{key: k, diskBacked: diskBacked}
				}
			}
		case contracts.TIToolResult:
			var tr ToolResultPayload
			if !decodePayload(item.Payload, &tr) {
				continue
			}
			if pr, ok := pending[tr.ToolCallID]; ok {
				ledger.RecordRead(step, pr.key, item.Seq, hashBytes([]byte(tr.Content)), len(tr.Content), pr.diskBacked)
				delete(pending, tr.ToolCallID)
			}
		}
	}

	// fs-watcher staleness (§2): apply observed disk changes for
	// disk-backed keys the ledger now knows about.
	for _, e := range ledger.Entries() {
		if !e.DiskBacked {
			continue
		}
		if fc, ok := m.fsObserved[e.Key.ID]; ok {
			ledger.ApplyFSChange(e.Key, fc.Kind, fc.ContentHash)
		}
	}

	// Pass 2: readmit-on-mention (§3b trigger 2) — a cheap string match
	// over keyed paths in agent messages / tool results (command output,
	// drift/diagnostic reports). A mention of a STALE, disk-backed key
	// reads disk itself (§3b re-admission source 2: "no model step is
	// burned"); a mention of a stale key with no disk ground truth, or a
	// valid tracked key, re-admits for free (sources 1/3).
	finalStep := len(turnInput) - 1
	// appendix: extra text appended to the TRIGGERING item's own rendered
	// content (§3b source 2: "delivered fresh content appended to the
	// triggering tool result… no model step is burned") — keyed by the
	// triggering item's index, since the ledger's live-copy Seq alone
	// can't point INTO a non-keyed message.
	appendix := make(map[int]string)
	if m.cfg.ReadmitOnMention {
		known := ledger.Entries()
		for i, item := range turnInput {
			text := scanText(item)
			if text == "" {
				continue
			}
			for _, e := range known {
				if !strings.Contains(text, e.Key.ID) {
					continue
				}
				_, src := ledger.Readmit(i, e.Key)
				switch src {
				case ReadmitFreeUnstub, ReadmitTrackedNoGroundTruth:
					m.lastEvents = append(m.lastEvents, NewCurationReadmittedEvent(threadID, e.Key))
				case ReadmitNeedsDiskRead:
					if m.diskReader == nil {
						continue // fail closed (ErrNoDiskReader documents the gap; Assemble itself never errors on this)
					}
					data, hash, err := m.diskReader.ReadFile(e.Key.ID)
					if err != nil {
						continue
					}
					ledger.RecordRead(i, e.Key, item.Seq, hash, len(data), true)
					// §3a: per-item cap BEFORE the budget episode. Re-admitting
					// the raw disk content would inject an arbitrarily large file
					// in full — and re-inject it every Assemble while stale —
					// defeating MaxRetainBytes and the whole curation budget.
					appendix[i] += fmt.Sprintf("\n[readmitted %s (re-read for current content): %s]", e.Key.ID, capText(string(data), m.cfg.MaxRetainBytes))
					m.lastEvents = append(m.lastEvents, NewCurationReadmittedEvent(threadID, e.Key))
				}
			}
		}
	}

	// One eviction episode over the final state.
	budget := m.cfg.BudgetBytes(m.model.ContextWindow)
	if finalStep < 0 {
		finalStep = 0
	}
	evict := ledger.RunEvictionEpisode(finalStep, budget)
	if len(evict.Demoted) > 0 {
		m.lastEvents = append(m.lastEvents, NewCurationDemotedEvent(threadID, evict.Demoted, evict.FreedBytes))
	}
	ledger.EnforceTrackedBound()

	// Render.
	var out []contracts.AssembledMessage
	if m.fragments != nil {
		out = append(out, m.fragments()...)
	}

	recentRank := unkeyedRecentRank(turnInput, m.cfg.Keys)

	emit := func(i int, role contracts.Role, content string) {
		out = append(out, contracts.AssembledMessage{Role: role, Content: content + appendix[i]})
	}

	for i, item := range turnInput {
		switch item.Type {
		case contracts.TIUserMessage, contracts.TIAgentMessage:
			role := contracts.RoleUser
			if item.Type == contracts.TIAgentMessage {
				role = contracts.RoleAssistant
			}
			emit(i, role, messageText(item))

		case contracts.TIToolCall:
			var tc ToolCallPayload
			if !decodePayload(item.Payload, &tc) {
				continue
			}
			mapping, keyed := m.cfg.Keys[tc.ToolName]
			if !keyed {
				emit(i, contracts.RoleAssistant, capText(fmt.Sprintf("tool_call %s %s", tc.ToolName, string(tc.Args)), m.cfg.MaxRetainBytes))
				continue
			}
			id := argStringAny(tc.Args, mapping.KeyArg)
			k := Key{Domain: mapping.Domain, ID: id}
			if mapping.Class == ClassEdit {
				emit(i, contracts.RoleAssistant, fmt.Sprintf("tool_call %s %s", tc.ToolName, string(tc.Args)))
				continue
			}
			if mapping.Class != ClassWrite {
				emit(i, contracts.RoleAssistant, fmt.Sprintf("tool_call %s path=%s", tc.ToolName, id))
				continue
			}
			content := argString(tc.Args, "content")
			emit(i, contracts.RoleAssistant, renderKeyed(ledger, k, item.Seq, content))

		case contracts.TIToolResult:
			var tr ToolResultPayload
			if !decodePayload(item.Payload, &tr) {
				continue
			}
			k, keyed := resolveResultKey(turnInput, i, m.cfg.Keys)
			if keyed {
				emit(i, contracts.RoleUser, renderKeyed(ledger, k, item.Seq, tr.Content))
				continue
			}
			// Unkeyed (tier 4): last KeepOthers verbatim (capped), older stubbed.
			content := tr.Content
			if rank, ok := recentRank[i]; ok && rank < m.cfg.KeepOthers {
				emit(i, contracts.RoleUser, capText(content, m.cfg.MaxRetainBytes))
			} else {
				emit(i, contracts.RoleUser, fmt.Sprintf("[older result: %s — %d chars, stubbed]", tr.ToolName, len(content)))
			}
		}
	}

	rawChars := 0
	for _, a := range out {
		rawChars += len(a.Content)
	}
	m.lastRawChars = rawChars
	m.lastEstimate = m.estimator.EstimateTokens(rawChars)
	return out, nil
}

// renderKeyed decides one keyed item's rendered content per §2/§3b: the
// live copy renders in full (subject to the ledger's already-applied
// per-item truncation), everything else stubs.
func renderKeyed(ledger *Ledger, k Key, seq int64, fullContent string) string {
	e, ok := ledger.Get(k)
	if !ok {
		return fullContent
	}
	if e.Stale {
		return "[modified since this read: re-read for current content]"
	}
	if e.Seq != seq {
		return fmt.Sprintf("[superseded by later write/read; %d chars]", len(fullContent))
	}
	if e.Tier == TierTracked {
		return fmt.Sprintf("[working set: %s demoted (untouched, %d bytes) — tracked; touch it or re-read to restore]", k.ID, e.SizeBytes)
	}
	body := fullContent
	if e.Truncated {
		body = capText(fullContent, e.SizeBytes)
	}
	if e.NoGroundTruth {
		// §3b source 3: served with a provenance marker — it is the only
		// truth there is (no disk/web/MCP source to re-read from).
		return "[re-admitted — no fresher source; tracked copy is current truth]\n" + body
	}
	return body
}

// resolveResultKey looks back from a tool_result at index i to the
// tool_call it answers (matching ToolCallID), reporting whether that call
// was for a keyed tool.
func resolveResultKey(items []contracts.ThreadItem, i int, keys map[string]KeyMapping) (Key, bool) {
	var tr ToolResultPayload
	if !decodePayload(items[i].Payload, &tr) {
		return Key{}, false
	}
	for j := i - 1; j >= 0; j-- {
		if items[j].Type != contracts.TIToolCall {
			continue
		}
		var tc ToolCallPayload
		if !decodePayload(items[j].Payload, &tc) {
			continue
		}
		if tc.ID != tr.ToolCallID {
			continue
		}
		mapping, ok := keys[tc.ToolName]
		if !ok || mapping.Class != ClassRead {
			return Key{}, false
		}
		return Key{Domain: mapping.Domain, ID: argStringAny(tc.Args, mapping.KeyArg)}, true
	}
	return Key{}, false
}

// unkeyedRecentRank ranks unkeyed tool_result items by recency (0 =
// most recent) — used to enforce KeepOthers (§1 tier 4).
func unkeyedRecentRank(items []contracts.ThreadItem, keys map[string]KeyMapping) map[int]int {
	var idxs []int
	for i, item := range items {
		if item.Type != contracts.TIToolResult {
			continue
		}
		if _, keyed := resolveResultKey(items, i, keys); keyed {
			continue
		}
		idxs = append(idxs, i)
	}
	rank := make(map[int]int, len(idxs))
	for r, idx := range idxs {
		rank[idx] = len(idxs) - 1 - r
	}
	return rank
}

func messageText(item contracts.ThreadItem) string {
	switch v := item.Payload.(type) {
	case string:
		return v
	case map[string]any:
		if t, ok := v["text"].(string); ok {
			return t
		}
	}
	return ""
}

// scanText is messageText widened to also cover tool_result content —
// the §3b trigger-2 mention scan runs over messages AND tool output
// (command output, drift/diagnostic reports are tool results).
func scanText(item contracts.ThreadItem) string {
	if item.Type == contracts.TIToolResult {
		var tr ToolResultPayload
		if decodePayload(item.Payload, &tr) {
			return tr.Content
		}
		return ""
	}
	return messageText(item)
}

// ResolvePartialSpan is the §3b partial-re-admission entry point wired to
// this Manager's configured SpanIndexer (nil -> line-window fallback) and
// PartialThreshold. Automatically DETECTING a locus from an arbitrary
// tool payload (an edit range, a symbol named in a diagnostic) is
// turn-engine/payload-format territory not yet defined anywhere in the
// repo (see payload.go's ambiguity note) — this unit proves the seam
// itself: given a locus, resolve the span to re-admit.
func (m *Manager) ResolvePartialSpan(content []byte, locus Locus) (Span, bool) {
	return ResolveSpan(m.spanIndexer, content, locus, m.cfg.PartialThreshold)
}

func capText(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	// Head-truncated with an idempotent marker (§3a) — truncating an
	// already-truncated string reproduces the same result (idempotent:
	// the marker itself is short and re-truncation is a no-op past it).
	return fmt.Sprintf("[truncated: %d of %d bytes]\n%s", maxBytes, len(s), s[:maxBytes])
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Observe implements contracts.ContextManager: folds actual usage against
// the last Assemble's raw estimate into the correction factor (§3a).
func (m *Manager) Observe(u contracts.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.estimator.Observe(m.lastRawChars, u.Input+u.Cached)
}

// Compact implements contracts.ContextManager. This manager curates
// continuously WITHIN Assemble (the LRU episode), so Compact is the
// documented no-op case (context spec §1: "may be a no-op for models that
// manage continuously rather than in compaction episodes") — it exists to
// fire the Pre/PostCompact hooks (contract 2) and report the current
// estimate for the wire events (contract 4), not to do additional
// curation work. Dialogue summarization (§5, last resort) is explicitly
// out of this unit's scope (ground rules: "the turn-engine that CONSUMES
// ContextManager… is another unit") — a future Manager wired with a
// summarizer alias would do real work here instead of NoOp.
func (m *Manager) Compact(trigger contracts.CompactionTrigger) (contracts.CompactionResult, error) {
	halted := m.hooks.RunPreCompact(trigger)
	m.mu.Lock()
	before := m.lastEstimate
	m.mu.Unlock()
	result := contracts.CompactionResult{
		Trigger:      trigger,
		TokensBefore: before,
		TokensAfter:  before,
		NoOp:         true,
	}
	if halted {
		return result, nil
	}
	m.hooks.RunPostCompact(result)
	return result, nil
}

// Status implements contracts.ContextManager (contract 8).
func (m *Manager) Status() contracts.ContextStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	window := m.model.ContextWindow
	remaining := 1.0
	if window > 0 {
		remaining = 1.0 - float64(m.lastEstimate)/float64(window)
		if remaining < 0 {
			remaining = 0
		}
	}
	return contracts.ContextStatus{
		EffectiveWindow:  window,
		CurrentEstimate:  m.lastEstimate,
		PercentRemaining: remaining,
	}
}

// DrainEvents returns and clears the curation view-events (§7 point 2:
// thread.curation.demoted/readmitted) emitted by the most recent Assemble
// call — outside contracts.ContextManager's fixed signature (Assemble only
// returns messages), the same way EvToolLoaded etc. ride the io seam
// out-of-band from their triggering call in every other package.
func (m *Manager) DrainEvents() []contracts.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev := m.lastEvents
	m.lastEvents = nil
	// Stable + a deterministic tiebreak: two events of the same Type
	// (e.g. two demotions) must have a fixed order for byte-identical
	// assembly across runs (determinism is a §7 design pillar).
	sort.SliceStable(ev, func(i, j int) bool {
		if ev[i].Type != ev[j].Type {
			return ev[i].Type < ev[j].Type
		}
		return string(ev[i].Payload) < string(ev[j].Payload)
	})
	return ev
}
