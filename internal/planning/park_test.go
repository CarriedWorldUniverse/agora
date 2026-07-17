package planning

import (
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
)

func TestParkLog_ParkAndIsWaiting(t *testing.T) {
	store := newMemThread(t, "th_park1")
	log := NewParkLog(store)

	q := contracts.QuestionAsked{ID: "q_1", Source: contracts.QuestionFromAgent, Blocking: true}

	if _, waiting, err := log.IsWaiting("th_park1"); err != nil || waiting {
		t.Fatalf("IsWaiting before Park = (%v, %v); want (_, false)", waiting, err)
	}

	if err := log.Park("th_park1", q, time.Unix(1, 0).UTC(), "agent:builder"); err != nil {
		t.Fatalf("Park: %v", err)
	}

	state, waiting, err := log.IsWaiting("th_park1")
	if err != nil {
		t.Fatalf("IsWaiting after Park: %v", err)
	}
	if !waiting {
		t.Fatalf("IsWaiting after Park = false; want true")
	}
	if state.Question.ID != "q_1" || state.ThreadID != "th_park1" {
		t.Fatalf("IsWaiting state = %+v; want question q_1 on th_park1", state)
	}
}

func TestParkLog_ResumeUnparks(t *testing.T) {
	store := newMemThread(t, "th_park2")
	log := NewParkLog(store)

	q := contracts.QuestionAsked{ID: "q_2", Source: contracts.QuestionFromAgent, Blocking: true}
	if err := log.Park("th_park2", q, time.Unix(1, 0).UTC(), "agent:builder"); err != nil {
		t.Fatalf("Park: %v", err)
	}

	ans := contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "ghcr"}, By: "device:phone1"}
	if err := log.Resume("th_park2", "q_2", ans, time.Unix(2, 0).UTC(), "device:phone1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	_, waiting, err := log.IsWaiting("th_park2")
	if err != nil {
		t.Fatalf("IsWaiting after Resume: %v", err)
	}
	if waiting {
		t.Fatalf("IsWaiting after Resume = true; want false (unparked)")
	}
}

func TestParkLog_ResumeMismatchedQuestionErrors(t *testing.T) {
	store := newMemThread(t, "th_park3")
	log := NewParkLog(store)

	q := contracts.QuestionAsked{ID: "q_3", Source: contracts.QuestionFromAgent, Blocking: true}
	if err := log.Park("th_park3", q, time.Unix(1, 0).UTC(), "agent:builder"); err != nil {
		t.Fatalf("Park: %v", err)
	}

	ans := contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "wrong question"}, By: "device:phone1"}
	err := log.Resume("th_park3", "q_not_the_open_one", ans, time.Unix(2, 0).UTC(), "device:phone1")
	if !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("Resume() error = %v; want ErrNotWaiting", err)
	}

	// Still parked on the original question — a mismatched Resume never
	// silently un-parks (never-fabricate: no answer is accepted for a
	// question it wasn't given).
	state, waiting, err := log.IsWaiting("th_park3")
	if err != nil {
		t.Fatalf("IsWaiting: %v", err)
	}
	if !waiting || state.Question.ID != "q_3" {
		t.Fatalf("IsWaiting = (%+v, %v); want still parked on q_3", state, waiting)
	}
}

func TestParkLog_ResumeWithoutParkErrors(t *testing.T) {
	store := newMemThread(t, "th_park4")
	log := NewParkLog(store)

	ans := contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "n/a"}, By: "device:phone1"}
	err := log.Resume("th_park4", "q_never_asked", ans, time.Unix(1, 0).UTC(), "device:phone1")
	if !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("Resume() on a never-parked thread error = %v; want ErrNotWaiting", err)
	}
}

func TestParkLog_SequentialParkResumeCycles(t *testing.T) {
	store := newMemThread(t, "th_park5")
	log := NewParkLog(store)

	q1 := contracts.QuestionAsked{ID: "q_a", Source: contracts.QuestionFromAgent, Blocking: true}
	if err := log.Park("th_park5", q1, time.Unix(1, 0).UTC(), "agent:builder"); err != nil {
		t.Fatalf("Park q_a: %v", err)
	}
	ans1 := contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "a"}, By: "device:phone1"}
	if err := log.Resume("th_park5", "q_a", ans1, time.Unix(2, 0).UTC(), "device:phone1"); err != nil {
		t.Fatalf("Resume q_a: %v", err)
	}

	q2 := contracts.QuestionAsked{ID: "q_b", Source: contracts.QuestionFromAgent, Blocking: true}
	if err := log.Park("th_park5", q2, time.Unix(3, 0).UTC(), "agent:builder"); err != nil {
		t.Fatalf("Park q_b: %v", err)
	}

	state, waiting, err := log.IsWaiting("th_park5")
	if err != nil {
		t.Fatalf("IsWaiting: %v", err)
	}
	if !waiting || state.Question.ID != "q_b" {
		t.Fatalf("IsWaiting = (%+v, %v); want parked on the LATEST open park (q_b)", state, waiting)
	}
}

// TestParkLog_SurvivesRestart is the DoD's durability check: park a thread
// on a LocalStore (real JSONL-backed persistence.ThreadStore), close the
// store (simulating daemon shutdown), reopen a fresh LocalStore instance
// over the same root, and confirm IsWaiting reloads the parked state purely
// by replaying what's on disk — no in-process state survives the "restart".
func TestParkLog_SurvivesRestart(t *testing.T) {
	root := t.TempDir()

	store1, err := persistence.NewLocalStore(root, persistence.Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	if err := store1.Create(contracts.ThreadMeta{
		ThreadID:  "th_restart",
		CreatedAt: time.Unix(0, 0).UTC(),
		Profile:   "dev",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	q := contracts.QuestionAsked{
		ID:     "q_restart",
		Source: contracts.QuestionFromAgent, Blocking: true,
		Args: contracts.QuestionArgs{Text: "which registry?"},
	}
	log1 := NewParkLog(store1)
	if err := log1.Park("th_restart", q, time.Unix(10, 0).UTC(), "agent:builder"); err != nil {
		t.Fatalf("Park: %v", err)
	}

	// Sanity: waiting before "restart".
	if _, waiting, err := log1.IsWaiting("th_restart"); err != nil || !waiting {
		t.Fatalf("IsWaiting before restart = (%v, %v); want (_, true)", waiting, err)
	}

	if err := store1.Close(); err != nil {
		t.Fatalf("Close store1: %v", err)
	}

	// "Restart": a brand-new LocalStore instance, same root, no shared
	// in-process state with store1/log1.
	store2, err := persistence.NewLocalStore(root, persistence.Config{})
	if err != nil {
		t.Fatalf("NewLocalStore (reopen): %v", err)
	}
	defer store2.Close()

	log2 := NewParkLog(store2)
	state, waiting, err := log2.IsWaiting("th_restart")
	if err != nil {
		t.Fatalf("IsWaiting after restart: %v", err)
	}
	if !waiting {
		t.Fatalf("IsWaiting after restart = false; want true (durable across restart)")
	}
	if state.Question.ID != "q_restart" || state.Question.Args.Text != "which registry?" {
		t.Fatalf("reloaded state = %+v; want question q_restart preserved", state)
	}

	// The reloaded log resolves the same way the original would have.
	ans := contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "ghcr"}, By: "device:phone1"}
	if err := log2.Resume("th_restart", "q_restart", ans, time.Unix(20, 0).UTC(), "device:phone1"); err != nil {
		t.Fatalf("Resume after restart: %v", err)
	}
	if _, waiting, err := log2.IsWaiting("th_restart"); err != nil || waiting {
		t.Fatalf("IsWaiting after resume-post-restart = (%v, %v); want (_, false)", waiting, err)
	}
}
