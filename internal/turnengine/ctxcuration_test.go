// ctxmgr wiring: internal/ctxmgr's curated-assembly library, plugged into
// the DIRECT-API turn path (see ctxcuration.go's package doc comment for
// the full scope/design rationale). This file's tests are the acceptance
// suite the ticket asks for: curated-vs-raw assembly, the claudesdk
// regression pin, working-set population from tool activity, /compact +
// context_length retry, the ctxmgr-error fallback, and determinism.

package turnengine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// readFileCall builds a bridle.ToolInvocation for read_file (KindRead,
// auto-allowed under defaultPolicy — no approval wiring needed).
func readFileCall(id, path string) bridle.ToolInvocation {
	args, _ := json.Marshal(map[string]string{"path": path})
	return bridle.ToolInvocation{ID: id, Name: toolrunner.ToolReadFile, Args: args}
}

// TestManager_CuratedTail_SupersedesStaleWrite is the CENTRAL acceptance
// test (A): a direct-API thread with a long fixture history — write_file
// twice on the SAME path — must curate the SessionTail, not raw-replay it:
// the FIRST write's args are superseded (stubbed), the SECOND (latest) full
// content is the one that reaches the wire.
func TestManager_CuratedTail_SupersedesStaleWrite(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "version-one-STALE")}},
		fake.Step{Text: "wrote v1"},
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("2", "note.txt", "version-two-LIVE")}},
		fake.Step{Text: "wrote v2"},
		fake.Step{Text: "probe"},
	)
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_curate_supersede"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, in, out, runErr := newTestManagerWithStore(t, "th_curate_supersede", store, provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()))

	runTurnAndDrain(t, m, in, out, runErr, "write v1")
	runTurnAndDrain(t, m, in, out, runErr, "write v2")
	runTurnAndDrain(t, m, in, out, runErr, "what's in note.txt?")

	msgs := provider.LastRequest().Messages
	var all strings.Builder
	for _, msg := range msgs {
		all.WriteString(msg.Content)
		all.WriteString("\n")
	}
	joined := all.String()

	if strings.Contains(joined, "version-one-STALE") {
		t.Fatalf("stale/superseded write content leaked into the curated tail: %q", joined)
	}
	if !strings.Contains(joined, "version-two-LIVE") {
		t.Fatalf("latest (live) write content missing from the curated tail: %q", joined)
	}
	if !strings.Contains(joined, "superseded") {
		t.Fatalf("expected a [superseded...] stub for the first write; got: %q", joined)
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_ClaudeSDKLane_UntouchedByCuration is the REGRESSION pin (A):
// the claudesdk-shaped subprocess lane never touches ctxmgr at all — no
// SessionTail (subprocess providers don't get one — see runOneTurn's
// `if ph.directAPI` guard), m.ctxMgr stays nil even after real tool
// activity, and the lowered request looks exactly as it did before this
// unit (no working-memory/curation text of any kind).
func TestManager_ClaudeSDKLane_UntouchedByCuration(t *testing.T) {
	provider := &recordingSubprocessProvider{SubprocessProvider: fake.NewSubprocessProvider(
		fake.SubprocessStep{Text: "ack", StopReason: bridle.StopReasonModelDone},
	)}
	m, in, out, runErr := newTestManagerWithStore(t, "th_claudesdk_untouched", nil, provider)

	runTurnAndDrain(t, m, in, out, runErr, "hello")

	if m.ctxMgr != nil {
		t.Fatal("m.ctxMgr got built for a subprocess (claudesdk-shaped) lane — curation must never touch it")
	}
	msgs := provider.lastReq.Messages
	if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Fatalf("lowered messages = %+v; want exactly [{user hello}] (untouched by curation)", msgs)
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_Observe_WriteThenReadPopulatesLedger (B): a write_file then a
// read_file of the SAME path across two turns populates ctxmgr's working-
// set ledger — asserted via a THIRD turn's curated tail: the read's fresh
// content is the live copy (not stubbed as stale/superseded), proving
// RecordWrite -> RecordRead tracked the key across turns.
func TestManager_Observe_WriteThenReadPopulatesLedger(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "obs.txt", "written-content")}},
		fake.Step{Text: "wrote it"},
		fake.Step{ToolCalls: []bridle.ToolInvocation{readFileCall("2", "obs.txt")}},
		fake.Step{Text: "read it back"},
		fake.Step{Text: "probe"},
	)
	m, in, out, runErr := newTestManagerWithStore(t, "th_observe", persistence.NewMemStore(), provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()))
	if err := m.store.Create(contracts.ThreadMeta{ThreadID: "th_observe"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	runTurnAndDrain(t, m, in, out, runErr, "write obs.txt")
	runTurnAndDrain(t, m, in, out, runErr, "read obs.txt")
	runTurnAndDrain(t, m, in, out, runErr, "probe")

	msgs := provider.LastRequest().Messages
	var all strings.Builder
	for _, msg := range msgs {
		all.WriteString(msg.Content)
		all.WriteString("\n")
	}
	joined := all.String()
	if !strings.Contains(joined, "written-content") {
		t.Fatalf("the observed key's content never reached the curated tail: %q", joined)
	}
	if strings.Contains(joined, "re-read for current content") {
		t.Fatalf("the key was marked stale even though nothing invalidated it: %q", joined)
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_Compact_ManualTriggersEventsAndMarker (C1): InConfig{Key:
// "compact"} fires the thread.compaction.started/.completed wire pair and
// persists a TICompactionMarker item — the /compact backend seam.
func TestManager_Compact_ManualTriggersEventsAndMarker(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_compact"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, in, out, runErr := newTestManagerWithStore(t, "th_compact", store, provider)

	runTurnAndDrain(t, m, in, out, runErr, "hello")

	in <- contracts.Input{Type: contracts.InConfig, Key: "compact"}

	started := recvWithin(t, out, testTimeout)
	if started.Type != contracts.EvCompactionStarted {
		t.Fatalf("first event after /compact = %+v; want thread.compaction.started", started)
	}
	completed := recvWithin(t, out, testTimeout)
	if completed.Type != contracts.EvCompactionCompleted {
		t.Fatalf("second event after /compact = %+v; want thread.compaction.completed", completed)
	}

	it, err := store.Resume("th_compact")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer it.Close()
	var sawMarker bool
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type == contracts.TICompactionMarker {
			sawMarker = true
		}
	}
	if !sawMarker {
		t.Fatal("no compaction_marker item persisted after manual /compact")
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_ContextLengthError_CompactsRetriesOnce (C2): a direct-api
// provider that fails ONCE with a context_length-shaped error then succeeds
// triggers exactly one compact-and-retry (context spec §2 contract 7) — the
// turn still completes, and the compaction wire pair fired in between.
func TestManager_ContextLengthError_CompactsRetriesOnce(t *testing.T) {
	provider := fake.NewProvider(
		fake.Step{Err: errContextLengthForTest{}},
		fake.Step{Text: "recovered"},
	)
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_ctxlen"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, in, out, runErr := newTestManagerWithStore(t, "th_ctxlen", store, provider)

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "a very long request"}

	var sawStarted, sawCompleted bool
	deadline := testTimeout
	ev := recvWithin(t, out, deadline) // turn.started
	if ev.Type != contracts.EvTurnStarted {
		t.Fatalf("first event = %+v; want turn.started", ev)
	}
	for {
		ev := recvWithin(t, out, deadline)
		switch ev.Type {
		case contracts.EvCompactionStarted:
			sawStarted = true
		case contracts.EvCompactionCompleted:
			sawCompleted = true
		case contracts.EvTurnCompleted:
			goto done
		case contracts.EvTurnFailed:
			t.Fatalf("turn failed instead of recovering via compact-and-retry")
		}
	}
done:
	if !sawStarted || !sawCompleted {
		t.Fatalf("compaction wire pair not observed (started=%v completed=%v)", sawStarted, sawCompleted)
	}
	if got := provider.StepsRemaining(); got != 0 {
		t.Fatalf("provider steps remaining = %d; want 0 (both scripted steps consumed: the failure + exactly one retry)", got)
	}

	endAndClose(t, in, out, runErr)
}

// errContextLengthForTest is a minimal error whose message matches
// isContextLengthError's conservative string classifier.
type errContextLengthForTest struct{}

func (errContextLengthForTest) Error() string {
	return "400 Bad Request: prompt exceeds the model's maximum context length"
}

// failingContextManager is a contracts.ContextManager double whose Assemble
// always errors — used to exercise the fallback-to-raw-tail path, since the
// real ctxmgr.Manager's Assemble never actually returns an error in its
// current reference implementation (see the Manager.ctxMgr field's doc
// comment).
type failingContextManager struct{}

func (failingContextManager) Assemble(string, []contracts.ThreadItem) ([]contracts.AssembledMessage, error) {
	return nil, errAssembleForTest{}
}
func (failingContextManager) Observe(contracts.Usage) {}
func (failingContextManager) Compact(trigger contracts.CompactionTrigger) (contracts.CompactionResult, error) {
	return contracts.CompactionResult{Trigger: trigger}, nil
}
func (failingContextManager) Status() contracts.ContextStatus { return contracts.ContextStatus{} }

type errAssembleForTest struct{}

func (errAssembleForTest) Error() string { return "ctxcuration_test: injected Assemble failure" }

// TestManager_CtxmgrError_FallsBackToRawTail (fallback): when the injected
// ContextManager's Assemble errors, the turn still completes using the RAW
// session-tail replay (never fails the turn over a curation error).
func TestManager_CtxmgrError_FallsBackToRawTail(t *testing.T) {
	provider := fake.NewProvider(
		fake.Step{Text: "turn one"},
		fake.Step{Text: "turn two"},
	)
	m, in, out, runErr := newTestManagerWithStore(t, "th_fallback", nil, provider, withContextManager(failingContextManager{}))

	runTurnAndDrain(t, m, in, out, runErr, "hi")
	runTurnAndDrain(t, m, in, out, runErr, "again")

	msgs := provider.LastRequest().Messages
	var all strings.Builder
	for _, msg := range msgs {
		all.WriteString(msg.Content)
		all.WriteString("\n")
	}
	if !strings.Contains(all.String(), "hi") {
		t.Fatalf("raw-tail fallback dropped turn 1's text; messages = %+v", msgs)
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_CuratedAssembly_Deterministic (determinism): calling
// assembleCuratedTail twice against the SAME accumulated item history
// yields byte-identical output — Assemble is a pure projection of
// (turnInput, config, model), never mutated state.
func TestManager_CuratedAssembly_Deterministic(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "det.txt", "stable-content")}},
		fake.Step{Text: "done"},
	)
	m, in, out, runErr := newTestManagerWithStore(t, "th_determinism", nil, provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()))

	runTurnAndDrain(t, m, in, out, runErr, "write det.txt")

	ph, err := m.harnessFor(nil)
	if err != nil {
		t.Fatalf("harnessFor: %v", err)
	}
	first, ok1 := m.assembleCuratedTail(ph)
	second, ok2 := m.assembleCuratedTail(ph)
	if !ok1 || !ok2 {
		t.Fatalf("assembleCuratedTail ok = (%v, %v); want (true, true)", ok1, ok2)
	}
	b1, _ := json.Marshal(first)
	b2, _ := json.Marshal(second)
	if string(b1) != string(b2) {
		t.Fatalf("two Assemble calls over the same items produced different output:\n1: %s\n2: %s", b1, b2)
	}

	endAndClose(t, in, out, runErr)
}
