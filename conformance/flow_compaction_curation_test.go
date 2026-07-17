// TestFlowCompactionCuration (blueprint §3.5): drives a REAL
// internal/ctxmgr.Manager end to end — Assemble under forced pressure (a
// tiny BudgetBytes vs two oversized synthetic file reads) produces a real
// eviction episode (thread.curation.demoted), then Compact (this Manager's
// documented continuous-curation no-op case, contracts/context.go) produces
// the compaction pair via the two NEW builders this unit adds
// (ctxmgr/events.go). No Input-await: the whole sequence runs off one
// trigger, matching the blueprint's "engine calls Assemble/Compact
// directly" description — no approval/question seam is in scope here, so
// flowEngine's await machinery isn't needed; this is the fourth "house
// inline-Engine" bespoke type in this suite (turn/approval already reuse
// ScriptedEngine/flowEngine; question_park_resume and this one are bespoke
// because their shape doesn't fit an Input-await script).
//
// Fixture tuning (blueprint §3.5/§6 resolution 5 — read ledger_test.go's
// convention first): HotSteps=0 (no key is immune purely by recency —
// TestLedger_EvictionHysteresis_TriggersAt100DemotesTo70 uses the same
// override), EvictTo=0.70 (the documented default). ContextWindow=1000
// tokens -> BudgetBytes = 1000 * 0.25(WsetBudgetFrac) * 4(BytesPerToken) =
// 1000 bytes, round and documented. Two 700-byte disk-backed reads (file/a
// touched at step 1, file/b at the FINAL step) sum to 1400 resident bytes,
// over the 1000-byte budget; file/b is exempt (touched at the assembly's
// own final step, so step-LastTouchStep==0<=HotSteps==0 keeps it immune)
// while file/a (touched two steps earlier) is the only eviction candidate —
// demoting it alone (1400-700=700) lands exactly at the 700-byte floor
// (EvictTo*budget), forcing EXACTLY one eviction episode, demoting EXACTLY
// one key, deterministically (no tie-break needed).
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/ctxmgr"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

type readItemPayload struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type compactionCurationEngine struct {
	threadID string
}

func (e *compactionCurationEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case _, ok := <-in:
		if !ok {
			return nil
		}
	}

	cfg := ctxmgr.DefaultConfig()
	cfg.HotSteps = 0
	cfg.EvictTo = 0.70
	model := contracts.ModelInfo{ID: "test-model", ContextWindow: 1000}
	mgr := ctxmgr.NewManager(cfg, model)

	contentA := strings.Repeat("A", 700)
	contentB := strings.Repeat("B", 700)
	items := []contracts.ThreadItem{
		{Seq: 1, Type: contracts.TIUserMessage, Payload: "please review the two large config files"},
		{Seq: 2, Type: contracts.TIToolCall, Payload: ctxmgr.ToolCallPayload{ToolName: "read_file", ID: "c1", Args: json.RawMessage(`{"path":"file/a.txt"}`)}},
		{Seq: 3, Type: contracts.TIToolResult, Payload: ctxmgr.ToolResultPayload{ToolCallID: "c1", ToolName: "read_file", Content: contentA}},
		{Seq: 4, Type: contracts.TIToolCall, Payload: ctxmgr.ToolCallPayload{ToolName: "read_file", ID: "c2", Args: json.RawMessage(`{"path":"file/b.txt"}`)}},
		{Seq: 5, Type: contracts.TIToolResult, Payload: ctxmgr.ToolResultPayload{ToolCallID: "c2", ToolName: "read_file", Content: contentB}},
	}

	events := []contracts.Event{
		newThreadStarted(e.threadID, threadStartedPayload{IdentityFP: "agora:k5xw3zjanfzsa2lt", Profile: "dev", WorkingDir: "/work/demo"}),
		newTurnStarted(e.threadID, "tu_0001"),
		{Type: contracts.EvItemStarted, ThreadID: e.threadID, TurnID: "tu_0001", Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemMCPToolCall}},
		{Type: contracts.EvItemCompleted, ThreadID: e.threadID, TurnID: "tu_0001", Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemMCPToolCall}, Payload: mustMarshalJSON(readItemPayload{Path: "file/a.txt", Bytes: len(contentA)})},
		{Type: contracts.EvItemStarted, ThreadID: e.threadID, TurnID: "tu_0001", Item: &contracts.ItemRef{Seq: 2, Type: contracts.ItemMCPToolCall}},
		{Type: contracts.EvItemCompleted, ThreadID: e.threadID, TurnID: "tu_0001", Item: &contracts.ItemRef{Seq: 2, Type: contracts.ItemMCPToolCall}, Payload: mustMarshalJSON(readItemPayload{Path: "file/b.txt", Bytes: len(contentB)})},
	}

	// The REAL seam call — Assemble runs the actual eviction-episode
	// algorithm (internal/ctxmgr/ledger.go's RunEvictionEpisode) over the
	// items above; DrainEvents returns whatever real curation events that
	// produced (deterministic under this fixture's tuning: exactly one
	// thread.curation.demoted).
	if _, err := mgr.Assemble(e.threadID, items); err != nil {
		return err
	}
	curationEvents := mgr.DrainEvents()
	for _, ev := range curationEvents {
		ev.ThreadID = e.threadID
		events = append(events, ev)
	}

	events = append(events, ctxmgr.NewCompactionStartedEvent(e.threadID, contracts.CompactManual))
	// The REAL seam call — Manager.Compact (this manager's documented
	// continuous-curation no-op case, contracts/context.go's ContextManager
	// doc comment) still returns a real, non-fabricated CompactionResult
	// (TokensBefore==TokensAfter==the real Assemble estimate).
	result, err := mgr.Compact(contracts.CompactManual)
	if err != nil {
		return err
	}
	completed := ctxmgr.NewCompactionCompletedEvent(e.threadID, result)
	events = append(events, completed)
	events = append(events, newTurnCompleted(e.threadID, "tu_0001", contracts.Usage{Input: 900, Output: 40}))

	for _, ev := range events {
		if !sendFlowEvent(ctx, out, ev) {
			return ctx.Err()
		}
	}
	return nil
}

func driveFlowCompactionCuration(t *testing.T) []byte {
	t.Helper()
	engine := &compactionCurationEngine{threadID: "th_0005"}
	in := strings.NewReader(`{"type":"user_message","text":"please review the two large config files"}` + "\n")
	var out, errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := agoraio.RunPipe(ctx, in, &out, &errBuf, engine, agoraio.PipeOptions{})
	if err != nil {
		t.Fatalf("RunPipe: %v", err)
	}
	if code != agoraio.ExitCompleted {
		t.Fatalf("exit code = %d, want ExitCompleted, stderr=%s", code, errBuf.String())
	}
	return out.Bytes()
}

func TestFlowCompactionCuration(t *testing.T) {
	got := driveFlowCompactionCuration(t)
	want := rawFlow(t, "compaction_curation.jsonl")
	if !bytes.Equal(got, want) {
		t.Fatalf("stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
