package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/ctxmgr"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// This file wires internal/ctxmgr (the tested 4-tier context-curation
// library) into the DIRECT-API turn path only — the claudesdk lane resumes
// a server-side session and never touches any of this (see runOneTurn's
// `if ph.directAPI` guard, unchanged for the subprocess lane).
//
// Scope shipped (architecture decisions, orchestrator-set, ground rule
// order A+B > C > D > E):
//
//	A. curated SessionTail assembly for direct-api turns, with an automatic
//	   fallback to the existing raw-replay tail on any ctxmgr error, and an
//	   opt-out Option (WithContextCuration(false)).
//	B. NOT a separate live "Observe(tool call)" call — ctxmgr's actual,
//	   tested Manager (manager.go's own doc comment) rebuilds its working-set
//	   ledger FRESH from turnInput on every Assemble call; there is no
//	   incremental tool-observation method on contracts.ContextManager (only
//	   Observe(usage), the token-estimate correction feed). So "B" is
//	   satisfied structurally: m.ctxItems accumulates every turn's
//	   TIToolCall/TIToolResult items (appendTurnToCtxItems, called from both
//	   of runOneTurn's persisting terminal branches) and the FULL growing
//	   list is handed to Assemble on the next turn, which is exactly what
//	   populates RecordRead/RecordWrite/staleness in the ledger.
//	C. context_length retry (compact-once-and-retry) + a manual-compaction
//	   backend seam (contracts.InConfig{Key:"compact"}). The TUI /compact
//	   slash verb itself (internal/tui) was NOT wired — see the ticket
//	   report for why (package-boundary plumbing: Backend interface +
//	   socket RPC + cmd/agora, out of this unit's reach).
//	D. StateFragments supplies the ALREADY-composed AppendSystemPrompt as a
//	   single cache-stable tier-1 fragment — the closest existing "composed
//	   system prompt pieces" seam this codebase has (profile.go has no
//	   richer skills/AGENTS.md/memory compose function yet); regenerated
//	   fresh on every Assemble call per the fixed contract.
//	E. Hooks bridge (ctxmgr.HookRunner -> internal/hooks) was NOT built —
//	   internal/turnengine has no existing hook-dispatch wiring at all (no
//	   hooks_wire.go exists in this tree to mirror), so this would be new,
//	   unscoped plumbing rather than "bridging an existing pattern". ctxmgr
//	   Manager runs with its default NoopHookRunner, exactly as documented
//	   for "a Manager built with no hooks".

// defaultCuratedContextWindow is the ContextWindow (tokens) ctxmgr.Manager
// budgets its resident working-set layer against, for Managers that have no
// richer per-model catalog lookup wired in (bridle's own model catalog
// (bridle.ModelInfo/Registry) is keyed differently — LaneID, not the plain
// model string this package carries — and threading a full registry lookup
// through NewManager/runOneTurn is out of this unit's scope). 200k tokens is
// the current Claude family's context window and a reasonable default for
// every direct-api model this package talks to today (local models behind
// the openai-compatible provider typically have SMALLER windows, which
// makes the curation budget conservative in that direction — curating
// harder than strictly necessary is the safe failure mode, not the
// dangerous one).
const defaultCuratedContextWindow int64 = 200_000

// WithContextCuration is the opt-out escape hatch (context spec's seam
// contract does not mandate curation — a trivial assemble-verbatim v0 is
// documented as valid): false disables ctxmgr entirely for this Manager,
// so every direct-api turn's SessionTail is built exactly as before this
// unit (the raw m.sessionTails replay). Default true.
func WithContextCuration(enabled bool) Option {
	return func(m *Manager) { m.ctxCurationEnabled = enabled }
}

// ensureCtxManager lazily builds this Manager's single per-thread
// contracts.ContextManager (curation spec: "one live copy… rebuildable by
// thread replay" — one Manager per Manager.threadID, not per provider,
// since contracts.ThreadItem is provider-neutral; per-provider SessionEvent
// conversion happens downstream in assembledMessagesToSessionTail). Tests
// can pre-set m.ctxMgr via the unexported withContextManager Option to
// inject a scripted double.
func (m *Manager) ensureCtxManager() contracts.ContextManager {
	if m.ctxMgr != nil {
		return m.ctxMgr
	}
	model := contracts.ModelInfo{ID: m.model, ContextWindow: defaultCuratedContextWindow}
	m.ctxMgr = ctxmgr.NewManager(ctxmgr.DefaultConfig(), model, ctxmgr.WithStateFragments(m.ctxStateFragments))
	return m.ctxMgr
}

// withContextManager is a TEST-ONLY Option (unexported — never part of this
// package's public surface) that pre-seeds m.ctxMgr, so ctxcuration_test.go
// can inject a scripted contracts.ContextManager double (e.g. one whose
// Assemble always errors) to exercise the fallback-on-error path. See the
// Manager.ctxMgr field's doc comment for why the field is interface-typed.
func withContextManager(cm contracts.ContextManager) Option {
	return func(m *Manager) { m.ctxMgr = cm }
}

// ctxStateFragments is the tier-1 StateFragments seam (D): the ALREADY
// composed system prompt (m.appendSystemPrompt — DevProfile's note today;
// whatever WithAppendSystemPrompt/WithProfile set it to) re-supplied fresh
// on every Assemble call, per the fixed contract ("regenerated, not
// summarized" — context-curation spec §1 tier 1 / §7 point 3). Marked
// CacheStable — it is the prefix every request shares.
func (m *Manager) ctxStateFragments() []contracts.AssembledMessage {
	if m.appendSystemPrompt == "" {
		return nil
	}
	return []contracts.AssembledMessage{{
		Role:        contracts.RoleSystem,
		Content:     m.appendSystemPrompt,
		CacheStable: true,
	}}
}

// seedCtxItemsFromStore populates m.ctxItems from the persisted thread,
// ONCE per Manager (NEX-798-style resume seeding, mirroring
// seedTailFromStore) — a fresh process resuming an existing thread must not
// start ctxmgr's ledger empty. Persisted items were written by persistTurn
// in THIS package's own on-disk shapes (toolCallItemPayload/
// toolResultItemPayload — {id,name,args}/{id,result,err}), which do not
// match ctxmgr's payload.go shapes ({tool_name,id,args_json}/
// {tool_call_id,tool_name,content,is_error}) — convertStoredItemForCtx is
// the shape-conversion seam payload.go's own doc comment anticipates
// ("whichever unit normalizes real provider tool items into ThreadItem.
// Payload should target this shape (or ctxmgr's decode helpers gain a
// second case)" — this unit chooses conversion-at-the-read-site over
// changing ctxmgr, since internal/ctxmgr is the tested reference
// implementation this ticket says not to touch without a genuine bug).
func (m *Manager) seedCtxItemsFromStore() {
	if m.ctxItemsSeeded {
		return
	}
	m.ctxItemsSeeded = true
	if m.store == nil {
		return
	}
	it, err := m.store.Resume(m.threadID)
	if err != nil {
		return
	}
	defer it.Close()
	var items []contracts.ThreadItem
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		conv, ok := convertStoredItemForCtx(item)
		if !ok {
			continue
		}
		items = append(items, conv)
		if conv.Seq > m.ctxSeq {
			m.ctxSeq = conv.Seq
		}
	}
	m.ctxItems = items
}

// convertStoredItemForCtx converts one PERSISTED ThreadItem (this package's
// on-disk payload shapes) into ctxmgr's expected shape. Returns ok=false for
// item types ctxmgr's Assemble does not interpret (approvals, hook
// outcomes, compaction markers, ...) — Assemble's own render loop silently
// skips unknown TIxxx types too, so dropping them here is equivalent, just
// cheaper (no decode attempt for types Assemble would ignore anyway).
func convertStoredItemForCtx(item contracts.ThreadItem) (contracts.ThreadItem, bool) {
	switch item.Type {
	case contracts.TIUserMessage, contracts.TIAgentMessage:
		return contracts.ThreadItem{Seq: item.Seq, TS: item.TS, Type: item.Type, Payload: decodeStoredText(item.Payload)}, true
	case contracts.TIToolCall:
		var tc toolCallItemPayload
		if !decodeAnyPayload(item.Payload, &tc) {
			return contracts.ThreadItem{}, false
		}
		return contracts.ThreadItem{
			Seq: item.Seq, TS: item.TS, Type: item.Type,
			Payload: ctxmgr.ToolCallPayload{ToolName: tc.Name, ID: tc.ID, Args: tc.Args},
		}, true
	case contracts.TIToolResult:
		var tr toolResultItemPayload
		if !decodeAnyPayload(item.Payload, &tr) {
			return contracts.ThreadItem{}, false
		}
		return contracts.ThreadItem{
			Seq: item.Seq, TS: item.TS, Type: item.Type,
			Payload: ctxmgr.ToolResultPayload{ToolCallID: tr.ID, Content: toolResultText(tr.Result), IsError: tr.Err != ""},
		}, true
	default:
		return contracts.ThreadItem{}, false
	}
}

// decodeAnyPayload re-marshals payload (a Go struct value from MemStore, or
// a map[string]any from a JSON-decoded store) into out — the same
// roundtrip convention durability_test.go's own decodePayload helper uses
// to read persisted items back in tests.
func decodeAnyPayload(payload any, out any) bool {
	b, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, out) == nil
}

// decodeStoredText extracts a persisted user_message/agent_message's text —
// a bare string (ctxmgr's own convertPersistedItemForCtx-adjacent messages
// already IN ctxmgr shape, e.g. appendTurnToCtxItems's own items) or a
// {"text":...} object (userMessageItemPayload/agentMessageItemPayload's
// on-disk shape) — matching ctxmgr's own messageText's two-case switch
// (payload.go), so an item built either way renders correctly.
func decodeStoredText(payload any) string {
	if s, ok := payload.(string); ok {
		return s
	}
	var p struct {
		Text string `json:"text"`
	}
	if decodeAnyPayload(payload, &p) {
		return p.Text
	}
	return ""
}

// appendTurnToCtxItems (B) records this turn's own items into m.ctxItems in
// ctxmgr's native shape directly — mirrors persistTurn's item ordering
// (user_message, then each tool_call/tool_result pair in order, then a
// trailing agent_message when FinalText is non-empty) but builds
// ctxmgr.ToolCallPayload/ToolResultPayload straight away (no round trip
// through this package's on-disk shapes, since these items never touch the
// store in this shape — persistTurn, called separately, is what writes the
// actual on-disk record). Runs for every turn ctxCurationEnabled — not
// gated on ph.directAPI — so the ledger stays accurate even across a /model
// switch mid-thread (a claudesdk turn's own tool activity still becomes
// keyed working-set history a LATER direct-api turn on the same thread can
// curate over).
func (m *Manager) appendTurnToCtxItems(input contracts.Input, result bridle.TurnResult, turnStartTS time.Time, sink *turnSink) {
	if !m.ctxCurationEnabled {
		return
	}
	nextSeq := func() int64 { m.ctxSeq++; return m.ctxSeq }
	m.ctxItems = append(m.ctxItems, contracts.ThreadItem{
		Seq: nextSeq(), TS: turnStartTS, Type: contracts.TIUserMessage, Payload: input.Text,
	})
	for _, tc := range result.ToolCalls {
		startTS, resultTS, ok := sink.toolCallEventTimes(tc.ID)
		if !ok {
			startTS, resultTS = turnStartTS, turnStartTS
		} else if resultTS.IsZero() {
			resultTS = startTS
		}
		m.ctxItems = append(m.ctxItems,
			contracts.ThreadItem{
				Seq: nextSeq(), TS: startTS, Type: contracts.TIToolCall,
				Payload: ctxmgr.ToolCallPayload{ToolName: tc.Name, ID: tc.ID, Args: tc.Args},
			},
			contracts.ThreadItem{
				Seq: nextSeq(), TS: resultTS, Type: contracts.TIToolResult,
				Payload: ctxmgr.ToolResultPayload{ToolCallID: tc.ID, Content: toolResultText(tc.Result), IsError: tc.Err != ""},
			},
		)
	}
	if result.FinalText != "" {
		closeTS := sink.turnDoneEventTime()
		if closeTS.IsZero() {
			closeTS = turnStartTS
		}
		m.ctxItems = append(m.ctxItems, contracts.ThreadItem{
			Seq: nextSeq(), TS: closeTS, Type: contracts.TIAgentMessage, Payload: result.FinalText,
		})
	}
}

// assembleCuratedTail (A's central seam) runs ctxmgr's Assemble over this
// thread's accumulated items and converts the curated projection into the
// []bridle.SessionEvent shape TurnRequest.SessionTail needs. ok=false on
// any ctxmgr error — the caller falls back to the raw sessionTails replay
// (contract: never fail a turn over a curation error).
func (m *Manager) assembleCuratedTail(ph providerHarness) ([]bridle.SessionEvent, bool) {
	m.seedCtxItemsFromStore()
	cm := m.ensureCtxManager()
	msgs, err := cm.Assemble(m.threadID, m.ctxItems)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turnengine: ctxmgr Assemble failed for thread %s: %v (falling back to raw session tail)\n", m.threadID, err)
		return nil, false
	}
	return assembledMessagesToSessionTail(msgs, ph.id), true
}

// assembledMessagesToSessionTail renders ctxmgr's []contracts.AssembledMessage
// (message-shaped text, per that type's own doc comment: "tool blocks etc.
// are the funnel's concern at the bridle Request layer") into
// []bridle.SessionEvent. RoleSystem entries (tier-1 state fragments, D) are
// DROPPED here rather than turned into a "system" tail entry: this
// Manager's req.AppendSystemPrompt already carries the exact same
// composed-system-prompt content (ctxStateFragments' source) through
// bridle's own system-prompt channel, so echoing it a second time onto the
// tail would duplicate it in every request rather than supplying it fresh.
func assembledMessagesToSessionTail(msgs []contracts.AssembledMessage, provID bridle.ProviderID) []bridle.SessionEvent {
	out := make([]bridle.SessionEvent, 0, len(msgs))
	for _, msg := range msgs {
		var role bridle.SessionRole
		switch msg.Role {
		case contracts.RoleAssistant:
			role = bridle.RoleAssistant
		case contracts.RoleSystem:
			continue
		default: // contracts.RoleUser and anything unrecognized
			role = bridle.RoleUser
		}
		out = append(out, bridle.SessionEvent{Provider: provID, Role: role, Content: msg.Content})
	}
	return out
}

// isContextLengthError conservatively string-matches a direct-api
// provider's returned error against the context_length family (C, context
// spec §2 contract 7). bridle's Harness.RunTurn has no typed ErrorClass on
// its (TurnResult, error) return today — contracts.ErrContextLength /
// bridle's own ErrorClassContextLength exist only on the Stream-facing
// event vocabulary, unreached from this call site — so this is the
// documented fallback ("match the provider error string conservatively").
func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{
		"context_length", "context length", "context window",
		"maximum context", "too many tokens", "prompt is too long",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// compactionMarkerPayload is the persisted TICompactionMarker item's body
// (curation spec §7 point 1: "a compaction marker MAY be appended").
type compactionMarkerPayload struct {
	Trigger      contracts.CompactionTrigger `json:"trigger"`
	TokensBefore int64                       `json:"tokens_before"`
	TokensAfter  int64                       `json:"tokens_after"`
}

// runCompactionEpisode (C) runs one ctxmgr.Manager.Compact call, emits the
// thread.compaction.started/.completed wire pair (context spec §2 contract
// 4) around it, and persists a compaction_marker item — to the store (when
// configured) AND to m.ctxItems, so a LATER Assemble call in this same
// process sees the marker too. Used by BOTH the manual /compact backend
// seam (InConfig{Key:"compact"}) and the automatic context_length retry
// path in runOneTurn.
//
// reassemble, when non-nil, rebuilds the request the caller is about to
// send. It runs BETWEEN Compact and the completed event on purpose — see
// the comment at the call below.
func (m *Manager) runCompactionEpisode(trigger contracts.CompactionTrigger, turnID string, ts time.Time, emit func(contracts.Event), reassemble func()) contracts.CompactionResult {
	cm := m.ensureCtxManager()
	started := ctxmgr.NewCompactionStartedEvent(m.threadID, trigger)
	started.TurnID = turnID
	emit(started)

	result, err := cm.Compact(trigger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turnengine: ctxmgr Compact failed for thread %s: %v\n", m.threadID, err)
	}

	// Compact ARMS the trim; this manager curates inside Assemble, so the
	// reduction does not exist until the caller reassembles. Doing that here
	// — before the completed event and the persisted marker — is what makes
	// TokensAfter a measured number instead of a placeholder equal to
	// TokensBefore, which is what the marker used to record (agora#134).
	//
	// nil for the manual /compact path: it runs BETWEEN turns, with no
	// request in hand to rebuild, so the trim lands on the next turn's
	// assembly and this episode honestly reports no measured reduction.
	if reassemble != nil {
		reassemble()
		if after := cm.Status().CurrentEstimate; after > 0 {
			result.TokensAfter = after
			result.NoOp = after >= result.TokensBefore
		}
	}

	completed := ctxmgr.NewCompactionCompletedEvent(m.threadID, result)
	completed.TurnID = turnID
	emit(completed)

	item := contracts.ThreadItem{
		TS:   ts,
		Type: contracts.TICompactionMarker,
		Payload: compactionMarkerPayload{
			Trigger:      result.Trigger,
			TokensBefore: result.TokensBefore,
			TokensAfter:  result.TokensAfter,
		},
	}
	if m.store != nil {
		if aerr := m.store.Append(m.threadID, []contracts.ThreadItem{item}); aerr != nil {
			fmt.Fprintf(os.Stderr, "turnengine: persist compaction marker for thread %s: %v\n", m.threadID, aerr)
		}
	}
	m.ctxSeq++
	item.Seq = m.ctxSeq
	m.ctxItems = append(m.ctxItems, item)
	return result
}

// runManualCompact (C1) is the /compact backend seam: Manager.Run's InConfig
// case calls this for Input{Type:InConfig, Key:"compact"} — always with
// trigger=CompactManual, and only when NO turn is in flight (contract #5:
// "a running turn is never interrupted by auto-compaction; the manager acts
// between sampling requests… or between turns" — Run's caller only reaches
// this when turnCancel == nil, enforced at the call site, not here).
func (m *Manager) runManualCompact(ctx context.Context, out chan<- contracts.Event) {
	emit := func(ev contracts.Event) {
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}
	m.runCompactionEpisode(contracts.CompactManual, "", m.now(), emit, nil)
}
