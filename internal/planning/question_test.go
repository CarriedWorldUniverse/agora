package planning

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestQuestionLog_Ask_InteractiveParks(t *testing.T) {
	store := newMemThread(t, "th_q1")
	log := NewQuestionLog(store)

	out, err := log.Ask(AskRequest{
		ThreadID: "th_q1",
		Args:     contracts.QuestionArgs{Text: "which registry?"},
		Source:   contracts.QuestionFromAgent,
		Blocking: true,
		Context:  ContextInteractive,
		TS:       time.Unix(1, 0).UTC(),
		Identity: "agent:builder",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if out.Disposition != DispositionPark {
		t.Fatalf("Disposition = %q; want park", out.Disposition)
	}
	if out.Parked == nil {
		t.Fatalf("Parked = nil; want the waiting-on-answer record")
	}
	if out.Terminate != nil || out.Bubble != nil {
		t.Fatalf("Terminate/Bubble should be nil for a park outcome: %+v", out)
	}
	if out.Parked.Question.ID != out.Question.ID {
		t.Fatalf("Parked.Question.ID = %q; want %q", out.Parked.Question.ID, out.Question.ID)
	}

	_, waiting, err := log.park.IsWaiting("th_q1")
	if err != nil || !waiting {
		t.Fatalf("thread not durably parked after Ask: waiting=%v err=%v", waiting, err)
	}
}

func TestQuestionLog_Ask_DispatchPodDiesHonestly(t *testing.T) {
	store := newMemThread(t, "th_q2")
	log := NewQuestionLog(store)

	out, err := log.Ask(AskRequest{
		ThreadID: "th_q2",
		Args:     contracts.QuestionArgs{Text: "which registry?"},
		Source:   contracts.QuestionFromAgent,
		Blocking: true,
		Context:  ContextDispatchPod,
		TS:       time.Unix(1, 0).UTC(),
		Identity: "agent:builder",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if out.Disposition != DispositionDieHonestly {
		t.Fatalf("Disposition = %q; want die_honestly", out.Disposition)
	}
	if out.Terminate == nil {
		t.Fatalf("Terminate = nil; want the blocked:needs-input shape")
	}
	if out.Terminate.Question.ID != out.Question.ID || out.Terminate.ThreadID != "th_q2" {
		t.Fatalf("Terminate = %+v; want it correlated to the question and thread", out.Terminate)
	}
	if out.Parked != nil || out.Bubble != nil {
		t.Fatalf("Parked/Bubble should be nil for a die-honestly outcome: %+v", out)
	}

	// A dying pod never parks: no durable waiting state left behind.
	_, waiting, err := log.park.IsWaiting("th_q2")
	if err != nil {
		t.Fatalf("IsWaiting: %v", err)
	}
	if waiting {
		t.Fatalf("thread parked after a die-honestly Ask; want no park record")
	}
}

func TestQuestionLog_Ask_SubagentBubbles(t *testing.T) {
	store := newMemThread(t, "th_q3")
	log := NewQuestionLog(store)

	out, err := log.Ask(AskRequest{
		ThreadID: "th_q3",
		Args:     contracts.QuestionArgs{Text: "which registry?"},
		Source:   contracts.QuestionFromAgent,
		Blocking: true,
		Context:  ContextSubagent,
		TS:       time.Unix(1, 0).UTC(),
		Identity: "agent:child",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if out.Disposition != DispositionBubble {
		t.Fatalf("Disposition = %q; want bubble", out.Disposition)
	}
	if out.Bubble == nil || out.Bubble.ID != out.Question.ID {
		t.Fatalf("Bubble = %+v; want the question carried up to the parent", out.Bubble)
	}
	if out.Parked != nil || out.Terminate != nil {
		t.Fatalf("Parked/Terminate should be nil for a bubble outcome: %+v", out)
	}
}

func TestQuestionLog_Ask_NonBlockingQueues(t *testing.T) {
	store := newMemThread(t, "th_q4")
	log := NewQuestionLog(store)

	out, err := log.Ask(AskRequest{
		ThreadID: "th_q4",
		Args:     contracts.QuestionArgs{Text: "fyi, going with ghcr unless you object"},
		Source:   contracts.QuestionFromAgent,
		Blocking: false,
		Context:  ContextInteractive,
		TS:       time.Unix(1, 0).UTC(),
		Identity: "agent:builder",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if out.Disposition != DispositionQueue {
		t.Fatalf("Disposition = %q; want queue", out.Disposition)
	}
	if out.Parked != nil || out.Terminate != nil || out.Bubble != nil {
		t.Fatalf("no side outcome should be set for queue: %+v", out)
	}
}

func TestQuestionLog_Ask_UnknownContextFailsClosed(t *testing.T) {
	store := newMemThread(t, "th_q5")
	log := NewQuestionLog(store)

	_, err := log.Ask(AskRequest{
		ThreadID: "th_q5",
		Args:     contracts.QuestionArgs{Text: "?"},
		Source:   contracts.QuestionFromAgent,
		Blocking: true,
		Context:  QuestionContext("nowhere"),
		TS:       time.Unix(1, 0).UTC(),
	})
	if !errors.Is(err, ErrUnknownContext) {
		t.Fatalf("Ask() error = %v; want ErrUnknownContext", err)
	}
}

func TestQuestionLog_Answer_UnparksThread(t *testing.T) {
	store := newMemThread(t, "th_q6")
	log := NewQuestionLog(store)

	out, err := log.Ask(AskRequest{
		ThreadID: "th_q6",
		Args:     contracts.QuestionArgs{Text: "which registry?"},
		Source:   contracts.QuestionFromAgent,
		Blocking: true,
		Context:  ContextInteractive,
		TS:       time.Unix(1, 0).UTC(),
		Identity: "agent:builder",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	ans := contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "ghcr"}, By: "device:phone1"}
	if err := log.Answer("th_q6", out.Question.ID, ans, time.Unix(2, 0).UTC(), "device:phone1"); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	_, waiting, err := log.park.IsWaiting("th_q6")
	if err != nil {
		t.Fatalf("IsWaiting: %v", err)
	}
	if waiting {
		t.Fatalf("thread still parked after Answer; want unparked")
	}
}

func TestQuestionLog_Answer_RequiresAttribution(t *testing.T) {
	store := newMemThread(t, "th_q7")
	log := NewQuestionLog(store)

	out, err := log.Ask(AskRequest{
		ThreadID: "th_q7",
		Args:     contracts.QuestionArgs{Text: "which registry?"},
		Source:   contracts.QuestionFromAgent,
		Blocking: true,
		Context:  ContextInteractive,
		TS:       time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	unattributed := contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "ghcr"}}
	err = log.Answer("th_q7", out.Question.ID, unattributed, time.Unix(2, 0).UTC(), "device:phone1")
	if !errors.Is(err, ErrUnattributedAnswer) {
		t.Fatalf("Answer() with blank By: error = %v; want ErrUnattributedAnswer", err)
	}

	// Never silently accepted: still parked.
	_, waiting, err := log.park.IsWaiting("th_q7")
	if err != nil || !waiting {
		t.Fatalf("thread unparked by an unattributed answer: waiting=%v err=%v", waiting, err)
	}
}

// TestQuestionLog_ConcurrentAsk exercises Ask/Answer from multiple
// goroutines across distinct threads, so `go test -race` has something to
// actually check: QuestionLog holds no mutable state of its own (every
// call routes straight through the ThreadStore, which owns its own
// locking), but this is the seam most likely to grow shared state later.
func TestQuestionLog_ConcurrentAsk(t *testing.T) {
	store := newMemStoreWithThreads(t, 8)
	log := NewQuestionLog(store)

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			threadID := fmt.Sprintf("th_concurrent_%d", i)
			_, err := log.Ask(AskRequest{
				ThreadID: threadID,
				Args:     contracts.QuestionArgs{Text: "concurrent ask"},
				Source:   contracts.QuestionFromAgent,
				Blocking: true,
				Context:  ContextInteractive,
				TS:       time.Unix(int64(i), 0).UTC(),
				Identity: "agent:builder",
			})
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d Ask: %v", i, err)
		}
	}
}
