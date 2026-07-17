package planning

// Regression tests for the U11 review gate (security-validator, HIGH TOCTOU +
// MED park-over-park; DeepSeek confirmed the single-threaded invariants clean).

import (
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func countType(t *testing.T, store contracts.ThreadStore, threadID string, typ contracts.ThreadItemType) int {
	t.Helper()
	it, err := store.Resume(threadID)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	n := 0
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type == typ {
			n++
		}
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	return n
}

// HIGH — concurrent Answer() on one parked question must resume it EXACTLY
// once (no double-resolve). The check-then-act (IsWaiting -> Resume) must be
// atomic per thread.
func TestQuestionLog_ConcurrentAnswerResumesOnce(t *testing.T) {
	store := newMemThread(t, "th_race")
	log := NewQuestionLog(store)
	out, err := log.Ask(AskRequest{ThreadID: "th_race", Args: contracts.QuestionArgs{Text: "q?"}, Source: contracts.QuestionFromAgent, Blocking: true, Context: ContextInteractive, TS: time.Unix(1, 0).UTC(), Identity: "agent:x"})
	if err != nil {
		t.Fatal(err)
	}
	qid := out.Question.ID

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = log.Answer("th_race", qid, contracts.Answer{By: "device-" + string(rune('a'+i))}, time.Unix(2, 0).UTC(), "device")
		}(i)
	}
	wg.Wait()

	if got := countType(t, store, "th_race", contracts.TIResumed); got != 1 {
		t.Fatalf("concurrent answers produced %d TIResumed items, want exactly 1 (double-resolve)", got)
	}
	if _, waiting, _ := log.park.IsWaiting("th_race"); waiting {
		t.Fatalf("thread still parked after being answered")
	}
}

// MED — Ask must refuse to park a second question over an unresolved one (that
// would orphan the first, whose later answer becomes a silent no-op). §4 "one
// thing at a time".
func TestQuestionLog_AskRefusesParkOverPark(t *testing.T) {
	store := newMemThread(t, "th_double")
	log := NewQuestionLog(store)
	if _, err := log.Ask(AskRequest{ThreadID: "th_double", Args: contracts.QuestionArgs{Text: "q1"}, Source: contracts.QuestionFromAgent, Blocking: true, Context: ContextInteractive, TS: time.Unix(1, 0).UTC(), Identity: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err := log.Ask(AskRequest{ThreadID: "th_double", Args: contracts.QuestionArgs{Text: "q2"}, Source: contracts.QuestionFromAgent, Blocking: true, Context: ContextInteractive, TS: time.Unix(2, 0).UTC(), Identity: "a"})
	if err == nil {
		t.Fatal("second Ask over an unresolved park should error (park-over-park orphans the first)")
	}
	// Exactly one question was parked (the first).
	if got := countType(t, store, "th_double", contracts.TIParked); got != 1 {
		t.Fatalf("park-over-park recorded %d TIParked items, want 1", got)
	}
}
