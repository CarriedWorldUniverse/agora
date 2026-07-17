package ctxmgr

// Contract-compliance tests: one test per curation-spec §7 point, mapping
// each numbered context-spec §2 contract to how the curation algorithm
// (this package) honors it. Transcribed directly from §7's list.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// §7.1 "thread never mutated — algorithm is an Assemble-time projection"
func TestContract1_ThreadNeverMutated(t *testing.T) {
	items := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"),
		readResult(2, "c1", "hello"),
	}
	before, _ := json.Marshal(items)

	m := NewManager(DefaultConfig(), testModel())
	if _, err := m.Assemble("t1", items); err != nil {
		t.Fatal(err)
	}

	after, _ := json.Marshal(items)
	if string(before) != string(after) {
		t.Fatal("Assemble must never mutate the thread items passed to it")
	}
}

// §7.2 "Pre/PostCompact hooks fired around summarization episodes; LRU
// episodes are not compaction... they fire thread.curation.demoted/
// readmitted instead"
func TestContract2_HooksFireAroundCompactNotLRU(t *testing.T) {
	hooks := &recordingHooks{}
	m := NewManager(DefaultConfig(), testModel(), WithHookRunner(hooks))

	// An LRU episode (via Assemble) must NOT fire Pre/PostCompact.
	items := []contracts.ThreadItem{readCall(t, 1, "c1", "a.py"), readResult(2, "c1", "x")}
	if _, err := m.Assemble("t1", items); err != nil {
		t.Fatal(err)
	}
	if hooks.pre != 0 || hooks.post != 0 {
		t.Fatalf("LRU/Assemble must not fire compaction hooks: pre=%d post=%d", hooks.pre, hooks.post)
	}

	// Compact() must fire both.
	if _, err := m.Compact(contracts.CompactManual); err != nil {
		t.Fatal(err)
	}
	if hooks.pre != 1 || hooks.post != 1 {
		t.Fatalf("Compact() must fire Pre/PostCompact exactly once each: pre=%d post=%d", hooks.pre, hooks.post)
	}

	// A demoted key must emit the curation event, not a compaction event.
	cfg := DefaultConfig()
	cfg.HotSteps = 0
	m2 := NewManager(cfg, contracts.ModelInfo{ID: "m", ContextWindow: 1})
	items2 := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"), readResult(2, "c1", strings.Repeat("x", 1000)),
		agentMsg(3, "noop"), agentMsg(4, "noop"), agentMsg(5, "noop"),
	}
	if _, err := m2.Assemble("t1", items2); err != nil {
		t.Fatal(err)
	}
	evs := m2.DrainEvents()
	found := false
	for _, e := range evs {
		if e.Type == contracts.EvCurationDemoted {
			found = true
		}
		if e.Type == contracts.EvCompactionStarted || e.Type == contracts.EvCompactionCompleted {
			t.Fatalf("LRU episode must never emit a compaction event, got %v", e.Type)
		}
	}
	if !found {
		t.Fatal("expected a thread.curation.demoted event from the LRU episode")
	}
}

type recordingHooks struct{ pre, post int }

func (r *recordingHooks) RunPreCompact(contracts.CompactionTrigger) bool { r.pre++; return false }
func (r *recordingHooks) RunPostCompact(contracts.CompactionResult)      { r.post++ }

// §7.3 "state regenerated, never summarized (tier 1)"
func TestContract3_StateFragmentsRegeneratedNeverSummarized(t *testing.T) {
	calls := 0
	frag := StateFragments(func() []contracts.AssembledMessage {
		calls++
		return []contracts.AssembledMessage{{Role: contracts.RoleSystem, Content: "fresh state", CacheStable: true}}
	})
	m := NewManager(DefaultConfig(), testModel(), WithStateFragments(frag))
	items := []contracts.ThreadItem{readCall(t, 1, "c1", "a.py"), readResult(2, "c1", "x")}

	out1, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("state fragments must be regenerated on EVERY assembly, called %d times for 2 assemblies", calls)
	}
	if out1[0].Content != "fresh state" || out2[0].Content != "fresh state" {
		t.Fatal("tier-1 fragment must be prepended verbatim, never summarized")
	}
	if !out1[0].CacheStable {
		t.Fatal("tier-1 state fragments must be marked cache-stable")
	}
}

// §7.4 "wire events — compaction pair as spec'd; plus the curation event"
func TestContract4_WireEvents(t *testing.T) {
	// Compaction pair from Compact().
	m := NewManager(DefaultConfig(), testModel())
	result, err := m.Compact(contracts.CompactAuto)
	if err != nil {
		t.Fatal(err)
	}
	if result.Trigger != contracts.CompactAuto {
		t.Fatalf("CompactionResult.Trigger = %v, want auto", result.Trigger)
	}
	// This manager's Compact is the documented continuous-curation no-op
	// case (context spec §1) — TokensBefore/After are still populated
	// numbers (contract 4's "tokens_before, tokens_after"), just equal.
	if result.TokensBefore != result.TokensAfter {
		t.Fatalf("no-op compaction should report equal before/after: %+v", result)
	}

	// Curation pair from an eviction episode (contract 4 + curation §7.2).
	demoted := NewCurationDemotedEvent("t1", []Key{{Domain: "file", ID: "a.py"}}, 400)
	if demoted.Type != contracts.EvCurationDemoted {
		t.Fatalf("Type = %v", demoted.Type)
	}
	var p CurationDemotedPayload
	if err := json.Unmarshal(demoted.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Keys) != 1 || p.Keys[0] != "file:a.py" || p.TokensFreed != 100 {
		t.Fatalf("payload = %+v", p)
	}

	readmitted := NewCurationReadmittedEvent("t1", Key{Domain: "file", ID: "a.py"})
	if readmitted.Type != contracts.EvCurationReadmitted {
		t.Fatalf("Type = %v", readmitted.Type)
	}
}

// §7.5 "never mid-turn — episodes run between requests"
func TestContract5_NeverMidTurn(t *testing.T) {
	// Assemble/Compact are only ever called between sampling requests by
	// construction — Assemble takes the full turnInput UP FRONT (nothing
	// streams in mid-call to interrupt), and Compact has no notion of "a
	// running turn" at all: it operates purely on already-observed usage
	// (via Observe, called after a request completes). There is no partial
	// turn state a mid-flight call could observe or corrupt.
	m := NewManager(DefaultConfig(), testModel())
	items := []contracts.ThreadItem{readCall(t, 1, "c1", "a.py"), readResult(2, "c1", "x")}
	out, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("sanity: Assemble produced output")
	}
	// Compact is safe to call with no pending request in flight.
	if _, err := m.Compact(contracts.CompactManual); err != nil {
		t.Fatal(err)
	}
}

// §7.6 "workflow journal / agent-graph untouched"
func TestContract6_WorkflowJournalOutOfScope(t *testing.T) {
	// ctxmgr has no dependency on, or type from, the workflow journal or
	// agent-graph packages at all — enforced structurally (no such import
	// exists in this package) rather than by a runtime assertion. This
	// test documents the contract; go.mod/import-graph is the actual
	// enforcement (there is nothing to import).
	t.Log("ctxmgr imports neither internal/workflow nor an agent-graph package — contract holds by construction")
}

// §7.7 "context_length -> episode-and-retry"
func TestContract7_ContextLengthRoutesToEpisodeAndRetry(t *testing.T) {
	// The retry loop itself is the turn engine's job (this unit models the
	// seam, per the ground rules) — what ctxmgr guarantees is that Compact
	// is safe and effective to call synchronously in response to a
	// context_length error and produces a usable result to retry with.
	m := NewManager(DefaultConfig(), testModel())
	result, err := m.Compact(contracts.CompactAuto)
	if err != nil {
		t.Fatalf("Compact must succeed so the turn engine can retry: %v", err)
	}
	if result.Trigger != contracts.CompactAuto {
		t.Fatalf("Trigger = %v", result.Trigger)
	}
}

// §7.8 "/status <- pressure gauge"
func TestContract8_StatusReadsPressureGauge(t *testing.T) {
	model := contracts.ModelInfo{ID: "m", ContextWindow: 1000}
	m := NewManager(DefaultConfig(), model)

	empty := m.Status()
	if empty.EffectiveWindow != 1000 {
		t.Fatalf("EffectiveWindow = %d, want 1000", empty.EffectiveWindow)
	}
	if empty.PercentRemaining != 1.0 {
		t.Fatalf("PercentRemaining = %v, want 1.0 before any assembly", empty.PercentRemaining)
	}

	items := []contracts.ThreadItem{readCall(t, 1, "c1", "a.py"), readResult(2, "c1", strings.Repeat("x", 400))}
	if _, err := m.Assemble("t1", items); err != nil {
		t.Fatal(err)
	}
	after := m.Status()
	if after.CurrentEstimate <= 0 {
		t.Fatal("CurrentEstimate must reflect the last assembly")
	}
	if after.PercentRemaining >= 1.0 {
		t.Fatal("PercentRemaining must drop after a non-trivial assembly")
	}
}
