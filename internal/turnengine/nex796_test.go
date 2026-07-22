// NEX-796: (1) ThreadItem.TS is EVENT time, not persist-time batch stamp —
// a tool call's persisted ts and the turn's closing message's persisted ts
// must differ and reflect actual event order; (2) a turn's usage payload
// persists as a closing turn_usage item; (3) resume/replay paths that walk
// persisted items skip turn_usage the same way they already skip tool
// items.

package turnengine

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// steppingClock returns a clock func that advances by step on every call —
// a deterministic stand-in for real wall-clock progression, letting a test
// assert per-item ts ordering without a real sleep (WithClock's documented
// use case). Each call is strictly later than the last.
func steppingClock(base time.Time, step time.Duration) func() time.Time {
	var n int64
	return func() time.Time {
		i := atomic.AddInt64(&n, 1)
		return base.Add(time.Duration(i) * step)
	}
}

// TestManager_Persist_EventTimeTS_DiffersPerItem: a turn with one tool call
// plus closing text persists FOUR items (user_message, tool_call,
// tool_result, agent_message — plus turn_usage) whose ts values are ALL
// distinct and in event order: user_message < tool_call <= tool_result <
// agent_message == turn_usage. Before NEX-796, persistTurn stamped every
// item in the batch with the SAME `now := time.Now()` — this test would
// have failed on the very first assertion (user vs tool_call ts equal).
func TestManager_Persist_EventTimeTS_DiffersPerItem(t *testing.T) {
	roots := managerTestRoots(t)
	args, err := json.Marshal(map[string]string{"path": "out.txt", "content": "hello"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolWriteFile, Args: args},
		}},
		fake.Step{Text: "wrote the file"},
	)
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_ts", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	clock := steppingClock(time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), 10*time.Millisecond)
	m, in, out, runErr := newTestManagerWithStore(t, "th_ts", store, provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}), WithClock(clock))

	runTurnAndDrain(t, m, in, out, runErr, "write out.txt")

	it, err := store.Resume("th_ts")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer it.Close()
	var items []contracts.ThreadItem
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		items = append(items, item)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator Err: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("got %d items; want 5 (user_message, tool_call, tool_result, agent_message, turn_usage): %+v", len(items), items)
	}
	byType := map[contracts.ThreadItemType]time.Time{}
	for _, it := range items {
		byType[it.Type] = it.TS
	}

	user := byType[contracts.TIUserMessage]
	call := byType[contracts.TIToolCall]
	result := byType[contracts.TIToolResult]
	agent := byType[contracts.TIAgentMessage]
	usage := byType[contracts.TITurnUsage]

	if !user.Before(call) {
		t.Fatalf("user_message ts %v must be BEFORE tool_call ts %v (event order)", user, call)
	}
	if call.After(result) {
		t.Fatalf("tool_call ts %v must be <= tool_result ts %v (event order)", call, result)
	}
	if !result.Before(agent) {
		t.Fatalf("tool_result ts %v must be BEFORE agent_message ts %v (event order)", result, agent)
	}
	if !agent.Equal(usage) {
		t.Fatalf("agent_message ts %v and turn_usage ts %v should share the turn's closing event time", agent, usage)
	}
	// The core NEX-796 regression: user_message and agent_message must NOT
	// share a ts — a single turn-boundary batch stamp put them all at the
	// SAME instant, hiding a 39-tool-call turn's real intra-turn timing.
	if user.Equal(agent) {
		t.Fatalf("user_message ts %v == agent_message ts %v; want DIFFERENT event times (NEX-796 regression)", user, agent)
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_Persist_TurnUsage: a turn whose provider reports usage
// persists a closing turn_usage item carrying that usage — reconstructable
// from the JSONL alone (spec §1's ccusage-style session/cost history
// requirement) — and store.Resume (used by seedTailFromStore/HistoryTail
// et al) skips it the same way it already skips tool_call/tool_result.
func TestManager_Persist_TurnUsage(t *testing.T) {
	wantUsage := bridle.Usage{
		InputTokens:              123,
		OutputTokens:             45,
		CacheReadInputTokens:     6,
		CacheCreationInputTokens: 7,
		ReasoningTokens:          8,
		CostUSD:                  0.042,
	}
	provider := fake.NewProvider(fake.Step{Text: "done", Usage: wantUsage})
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_usage", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, in, out, runErr := newTestManagerWithStore(t, "th_usage", store, provider,
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	runTurnAndDrain(t, m, in, out, runErr, "hi")

	it, err := store.Resume("th_usage")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer it.Close()
	var usageItem *contracts.ThreadItem
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type == contracts.TITurnUsage {
			cp := item
			usageItem = &cp
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator Err: %v", err)
	}
	if usageItem == nil {
		t.Fatal("no turn_usage item persisted")
	}
	var got contracts.Usage
	if err := decodePayload(usageItem.Payload, &got); err != nil {
		t.Fatalf("decode turn_usage payload: %v", err)
	}
	want := mapUsage(wantUsage)
	if got != want {
		t.Fatalf("turn_usage payload = %+v; want %+v", got, want)
	}

	// seedTailFromStore (a real resume-path consumer of store.Resume) must
	// not choke on / include the turn_usage item — only user/agent text
	// seeds the tail.
	ph := providerHarness{id: "fake-provider", directAPI: true, key: "th_usage_seed"}
	m.seedTailFromStore(ph)
	tail := m.sessionTails[ph.key]
	if len(tail) != 2 {
		t.Fatalf("seeded tail = %+v; want 2 entries (user + agent text only, turn_usage skipped)", tail)
	}
	if tail[0].Role != bridle.RoleUser || tail[0].Content != "hi" {
		t.Fatalf("tail[0] = %+v; want user 'hi'", tail[0])
	}
	if tail[1].Role != bridle.RoleAssistant || tail[1].Content != "done" {
		t.Fatalf("tail[1] = %+v; want assistant 'done'", tail[1])
	}

	endAndClose(t, in, out, runErr)
}
