package turnengine

import (
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// TestManager_ResumeSeedsDirectAPITail (NEX-798): a fresh Manager over a
// thread WITH persisted history must seed the direct-API SessionTail from the
// store — without this a resumed kimi/glm session has amnesia despite a
// complete JSONL. Tool items are skipped; only user/agent text seeds.
func TestManager_ResumeSeedsDirectAPITail(t *testing.T) {
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_seed", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Prior session's persisted exchange (as persistTurn writes it).
	if err := store.Append("th_seed", []contracts.ThreadItem{
		{TS: time.Now().UTC(), Type: contracts.TIUserMessage, Payload: map[string]any{"text": "the pack size is 32MiB"}},
		{TS: time.Now().UTC(), Type: contracts.TIToolCall, Payload: map[string]any{"id": "c1", "name": "run_command"}},
		{TS: time.Now().UTC(), Type: contracts.TIToolResult, Payload: map[string]any{"id": "c1", "result": "ok"}},
		{TS: time.Now().UTC(), Type: contracts.TIAgentMessage, Payload: map[string]any{"text": "noted, 32MiB"}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	provider := fake.NewProvider(fake.Step{Text: "still 32MiB"})
	_, in, out, runErr := newTestManagerWithStore(t, "th_seed", store, provider)

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "what size?"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}

	msgs := provider.LastRequest().Messages
	// Expect: seeded [user, assistant] + this turn's user (+ trailing ctxmap
	// memory message, stripped if present).
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" && len(msgs[n-1].Content) > 0 && msgs[n-1].Content[0] == '#' {
		msgs = msgs[:n-1]
	}
	if len(msgs) != 3 {
		t.Fatalf("messages = %d (%+v), want seeded user+assistant + new user", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "the pack size is 32MiB" {
		t.Fatalf("msgs[0] = %+v, want the persisted user message", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "noted, 32MiB" {
		t.Fatalf("msgs[1] = %+v, want the persisted agent reply (tool items skipped)", msgs[1])
	}
	if msgs[2].Role != "user" || msgs[2].Content != "what size?" {
		t.Fatalf("msgs[2] = %+v, want this turn's message", msgs[2])
	}
	endAndClose(t, in, out, runErr)
}

// TestManager_FreshThreadSeedsNothing: no prior items → tail stays empty (no
// phantom history).
func TestManager_FreshThreadSeedsNothing(t *testing.T) {
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_fresh", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	provider := fake.NewProvider(fake.Step{Text: "hello"})
	_, in, out, runErr := newTestManagerWithStore(t, "th_fresh", store, provider)
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	msgs := provider.LastRequest().Messages
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" && len(msgs[n-1].Content) > 0 && msgs[n-1].Content[0] == '#' {
		msgs = msgs[:n-1]
	}
	if len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Fatalf("messages = %+v, want just this turn's message", msgs)
	}
	endAndClose(t, in, out, runErr)
}
