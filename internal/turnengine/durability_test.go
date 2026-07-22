// U-C6/U-C7 (NEX-785): Manager durability — WithStore persistence of each
// turn's ThreadItems, and a stable per-thread bridle.SessionHandle so the
// claude-sdk lane resumes rather than restarting the conversation.

package turnengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// runTurnAndDrain sends a user_message through m and drains events until
// turn.completed, then ends the run cleanly. Fails the test if the turn
// never completes or Run returns an error.
func runTurnAndDrain(t *testing.T, m *Manager, in chan contracts.Input, out chan contracts.Event, runErr chan error, text string) {
	t.Helper()
	in <- contracts.Input{Type: contracts.InUserMessage, Text: text}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
}

func newTestManagerWithStore(t *testing.T, threadID string, store contracts.ThreadStore, provider bridle.Provider, opts ...Option) (*Manager, chan contracts.Input, chan contracts.Event, chan error) {
	t.Helper()
	allOpts := append([]Option{}, opts...)
	if store != nil {
		allOpts = append(allOpts, WithStore(store))
	}
	m := NewManager(threadID, provider, allOpts...)
	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()
	return m, in, out, runErr
}

func endAndClose(t *testing.T, in chan contracts.Input, out chan contracts.Event, runErr chan error) {
	t.Helper()
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}

// TestManager_Session_FreshThenResume: turn 1 on a fresh (no-prior-items)
// thread gets Session.New=true; turn 2 on the SAME Manager gets New=false
// — both carrying the STABLE derived session UUID (sessionIDFor(threadID), a
// valid-UUID requirement of the claude-sdk lane), identical across turns.
func TestManager_Session_FreshThenResume(t *testing.T) {
	provider := fake.NewProvider(
		fake.Step{Text: "turn one"},
		fake.Step{Text: "turn two"},
	)
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_sess", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, in, out, runErr := newTestManagerWithStore(t, "th_sess", store, provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001", "tu_0002"}}))

	runTurnAndDrain(t, m, in, out, runErr, "hi")
	got1 := provider.LastRequest().Session
	want1 := bridle.SessionHandle{ID: sessionIDFor("th_sess"), New: true}
	if got1 != want1 {
		t.Fatalf("turn 1 Session = %+v; want %+v", got1, want1)
	}
	if _, err := uuid.Parse(got1.ID); err != nil {
		t.Fatalf("session id %q is not a valid UUID (claude-sdk requires one): %v", got1.ID, err)
	}

	runTurnAndDrain(t, m, in, out, runErr, "again")
	got2 := provider.LastRequest().Session
	want2 := bridle.SessionHandle{ID: sessionIDFor("th_sess"), New: false}
	if got2 != want2 {
		t.Fatalf("turn 2 Session = %+v; want %+v", got2, want2)
	}
	if got2.ID != got1.ID {
		t.Fatalf("session id changed across turns: turn1=%q turn2=%q (resume needs a STABLE id)", got1.ID, got2.ID)
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_Session_NoStore_StillFlips: a Manager with NO WithStore
// still flips Session.New true -> false in-memory across turns (the flag
// is Manager-local bookkeeping, not store-dependent), and persists
// nothing (nil store, no Append calls possible).
func TestManager_Session_NoStore_StillFlips(t *testing.T) {
	provider := fake.NewProvider(
		fake.Step{Text: "turn one"},
		fake.Step{Text: "turn two"},
	)
	m, in, out, runErr := newTestManagerWithStore(t, "th_nostore", nil, provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001", "tu_0002"}}))

	runTurnAndDrain(t, m, in, out, runErr, "hi")
	if got := provider.LastRequest().Session; got != (bridle.SessionHandle{ID: sessionIDFor("th_nostore"), New: true}) {
		t.Fatalf("turn 1 Session = %+v; want New:true", got)
	}

	runTurnAndDrain(t, m, in, out, runErr, "again")
	if got := provider.LastRequest().Session; got != (bridle.SessionHandle{ID: sessionIDFor("th_nostore"), New: false}) {
		t.Fatalf("turn 2 Session = %+v; want New:false", got)
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_Session_ResumeFromExistingStoreItems: a Manager built
// WithStore(store) for a threadID that ALREADY has items in store (an
// earlier process ran turns on this thread) gets New=false on its very
// FIRST turn — the one-time prior-items probe correctly detects a resume.
func TestManager_Session_ResumeFromExistingStoreItems(t *testing.T) {
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_resume", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Append("th_resume", []contracts.ThreadItem{
		{Type: contracts.TIUserMessage, Payload: userMessageItemPayload{Text: "an earlier turn"}},
	}); err != nil {
		t.Fatalf("seed Append: %v", err)
	}

	provider := fake.NewProvider(fake.Step{Text: "resumed"})
	m, in, out, runErr := newTestManagerWithStore(t, "th_resume", store, provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	runTurnAndDrain(t, m, in, out, runErr, "continue")
	got := provider.LastRequest().Session
	want := bridle.SessionHandle{ID: sessionIDFor("th_resume"), New: false}
	if got != want {
		t.Fatalf("first-turn Session on a thread with prior items = %+v; want %+v (resume)", got, want)
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_Persist_UserToolAgent_Order: a turn with one tool call plus
// closing text persists, in order: TIUserMessage, TIToolCall, TIToolResult,
// TIAgentMessage, with the payloads the brief specifies. A second turn
// accumulates onto the same thread rather than overwriting it.
func TestManager_Persist_UserToolAgent_Order(t *testing.T) {
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
		fake.Step{Text: "second turn, no tools"},
	)
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_persist", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, in, out, runErr := newTestManagerWithStore(t, "th_persist", store, provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001", "tu_0002"}}))

	runTurnAndDrain(t, m, in, out, runErr, "write out.txt")

	it, err := store.Resume("th_persist")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
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
	if err := it.Close(); err != nil {
		t.Fatalf("iterator Close: %v", err)
	}

	if len(items) != 5 {
		t.Fatalf("got %d items after turn 1; want 5 (user_message, tool_call, tool_result, agent_message, turn_usage): %+v", len(items), items)
	}
	wantTypes := []contracts.ThreadItemType{
		contracts.TIUserMessage, contracts.TIToolCall, contracts.TIToolResult, contracts.TIAgentMessage, contracts.TITurnUsage,
	}
	for i, wt := range wantTypes {
		if items[i].Type != wt {
			t.Fatalf("item[%d].Type = %q; want %q (full: %+v)", i, items[i].Type, wt, items)
		}
	}

	var userPayload userMessageItemPayload
	if err := decodePayload(items[0].Payload, &userPayload); err != nil {
		t.Fatalf("decode user_message payload: %v", err)
	}
	if userPayload.Text != "write out.txt" {
		t.Fatalf("user_message text = %q; want %q", userPayload.Text, "write out.txt")
	}

	var callPayload toolCallItemPayload
	if err := decodePayload(items[1].Payload, &callPayload); err != nil {
		t.Fatalf("decode tool_call payload: %v", err)
	}
	if callPayload.ID != "1" || callPayload.Name != toolrunner.ToolWriteFile {
		t.Fatalf("tool_call payload = %+v; want id=1 name=%s", callPayload, toolrunner.ToolWriteFile)
	}

	var resultPayload toolResultItemPayload
	if err := decodePayload(items[2].Payload, &resultPayload); err != nil {
		t.Fatalf("decode tool_result payload: %v", err)
	}
	if resultPayload.ID != "1" {
		t.Fatalf("tool_result payload = %+v; want id=1", resultPayload)
	}
	if resultPayload.Err != "" {
		t.Fatalf("tool_result err = %q; want empty (write should succeed)", resultPayload.Err)
	}

	var agentPayload agentMessageItemPayload
	if err := decodePayload(items[3].Payload, &agentPayload); err != nil {
		t.Fatalf("decode agent_message payload: %v", err)
	}
	if agentPayload.Text != "wrote the file" {
		t.Fatalf("agent_message text = %q; want %q", agentPayload.Text, "wrote the file")
	}

	// Multi-turn accumulates: a second, tool-free turn appends its own
	// user_message + agent_message onto the SAME thread, on top of turn
	// 1's four items, rather than replacing them.
	runTurnAndDrain(t, m, in, out, runErr, "and now?")

	it2, err := store.Resume("th_persist")
	if err != nil {
		t.Fatalf("Resume (turn 2): %v", err)
	}
	var items2 []contracts.ThreadItem
	for {
		item, ok := it2.Next()
		if !ok {
			break
		}
		items2 = append(items2, item)
	}
	if err := it2.Close(); err != nil {
		t.Fatalf("iterator Close: %v", err)
	}
	if len(items2) != 8 {
		t.Fatalf("got %d items after turn 2; want 8 (5 + user_message + agent_message + turn_usage): %+v", len(items2), items2)
	}
	if items2[5].Type != contracts.TIUserMessage || items2[6].Type != contracts.TIAgentMessage || items2[7].Type != contracts.TITurnUsage {
		t.Fatalf("turn 2's items = %+v; want [user_message, agent_message, turn_usage]", items2[5:])
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_Persist_NoFinalText_NoAgentMessage: a tool-only turn (empty
// FinalText) persists user_message + tool_call/tool_result but NO trailing
// TIAgentMessage — an empty agent_message would misrepresent what the
// model actually said.
func TestManager_Persist_NoFinalText_NoAgentMessage(t *testing.T) {
	roots := managerTestRoots(t)
	args, err := json.Marshal(map[string]string{"path": "out.txt", "content": "hello"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolWriteFile, Args: args},
		}},
		fake.Step{Text: ""},
	)
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_notext", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m, in, out, runErr := newTestManagerWithStore(t, "th_notext", store, provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	runTurnAndDrain(t, m, in, out, runErr, "write out.txt")

	it, err := store.Resume("th_notext")
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
	if len(items) != 4 {
		t.Fatalf("got %d items; want 4 (user_message, tool_call, tool_result, turn_usage, NO agent_message): %+v", len(items), items)
	}
	wantTypes := []contracts.ThreadItemType{contracts.TIUserMessage, contracts.TIToolCall, contracts.TIToolResult, contracts.TITurnUsage}
	for i, wt := range wantTypes {
		if items[i].Type != wt {
			t.Fatalf("item[%d].Type = %q; want %q", i, items[i].Type, wt)
		}
	}

	endAndClose(t, in, out, runErr)
}

// TestManager_NoStore_PersistsNothingAndRunsFine: the DEFAULT (no
// WithStore) Manager runs a tool-using turn to completion exactly as
// before this unit — no panics, no persistence attempted (there is
// nothing to persist TO). Guards existing no-store behavior staying
// green.
func TestManager_NoStore_PersistsNothingAndRunsFine(t *testing.T) {
	roots := managerTestRoots(t)
	args, err := json.Marshal(map[string]string{"path": "out.txt", "content": "hello"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolWriteFile, Args: args},
		}},
		fake.Step{Text: "done"},
	)
	m, in, out, runErr := newTestManagerWithStore(t, "th_default", nil, provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))
	if m.store != nil {
		t.Fatalf("m.store = %v; want nil (default, no WithStore)", m.store)
	}

	runTurnAndDrain(t, m, in, out, runErr, "write out.txt")
	endAndClose(t, in, out, runErr)
}

// decodePayload round-trips a ThreadItem.Payload (any, as stored by
// MemStore) through JSON into dst — matching how a real ThreadStore
// (JSONL-backed LocalStore) would hand payloads back on Resume.
func decodePayload(payload any, dst any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
