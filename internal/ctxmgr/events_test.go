package ctxmgr

import (
	"encoding/json"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestNewCompactionEvents: the compaction pair U18 adds (the curation pair
// already existed; contracts_test.go's TestCompactAndCurationEvents-style
// coverage for it) — thread.compaction.started {trigger} /
// thread.compaction.completed {tokens_before,tokens_after}, per io spec §52.
func TestNewCompactionEvents(t *testing.T) {
	started := NewCompactionStartedEvent("t1", contracts.CompactManual)
	if started.Type != contracts.EvCompactionStarted || started.ThreadID != "t1" {
		t.Fatalf("started = %+v", started)
	}
	var sp CompactionStartedPayload
	if err := json.Unmarshal(started.Payload, &sp); err != nil {
		t.Fatal(err)
	}
	if sp.Trigger != contracts.CompactManual {
		t.Fatalf("payload = %+v, want trigger=manual", sp)
	}

	completed := NewCompactionCompletedEvent("t1", contracts.CompactionResult{
		Trigger: contracts.CompactManual, TokensBefore: 500, TokensAfter: 500, NoOp: true,
	})
	if completed.Type != contracts.EvCompactionCompleted || completed.ThreadID != "t1" {
		t.Fatalf("completed = %+v", completed)
	}
	var cp CompactionCompletedPayload
	if err := json.Unmarshal(completed.Payload, &cp); err != nil {
		t.Fatal(err)
	}
	if cp.TokensBefore != 500 || cp.TokensAfter != 500 {
		t.Fatalf("payload = %+v, want tokens_before=500 tokens_after=500", cp)
	}
}
