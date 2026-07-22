// NEX-796: turn_usage items must be invisible to the resume-history replay
// path — HistoryTail's job is "the text thread is the story" (its own doc
// comment), and a turn_usage item has no text to show.
package main

import (
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
)

// TestHistoryTail_SkipsTurnUsage: a thread with user/agent messages
// interleaved with turn_usage items yields ONLY the text entries —
// turn_usage falls through HistoryTail's `default: continue` the same way
// tool_call/tool_result already do.
func TestHistoryTail_SkipsTurnUsage(t *testing.T) {
	store := persistence.NewMemStore()
	threadID := "th_nex796_history"
	if err := store.Create(contracts.ThreadMeta{ThreadID: threadID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Append(threadID, []contracts.ThreadItem{
		{Type: contracts.TIUserMessage, Payload: map[string]any{"text": "hello"}},
		{Type: contracts.TITurnUsage, Payload: contracts.Usage{Input: 10, Output: 5}},
		{Type: contracts.TIAgentMessage, Payload: map[string]any{"text": "hi there"}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	b := &inProcessBackend{store: store}
	entries, elided, err := b.HistoryTail(threadID, 12)
	if err != nil {
		t.Fatalf("HistoryTail: %v", err)
	}
	if elided != 0 {
		t.Fatalf("elided = %d; want 0", elided)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries; want 2 (turn_usage must be skipped): %+v", len(entries), entries)
	}
	if entries[0].Role != "user" || entries[0].Text != "hello" {
		t.Fatalf("entries[0] = %+v; want {user, hello}", entries[0])
	}
	if entries[1].Role != "agent" || entries[1].Text != "hi there" {
		t.Fatalf("entries[1] = %+v; want {agent, hi there}", entries[1])
	}
}
