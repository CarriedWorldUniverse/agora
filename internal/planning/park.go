package planning

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// WaitingState is one thread's durable park record — reconstructed by
// replaying the thread's own persisted items, so it survives a daemon
// restart/detach without any separate index (§6 invariant 2: "a parked
// thread is durable state ... and visible").
type WaitingState struct {
	ThreadID string
	Question contracts.QuestionAsked
	ParkedAt time.Time
}

// parkedPayload / resumedPayload are the TIParked / TIResumed item payload
// shapes this package writes and reads back.
type parkedPayload struct {
	Question contracts.QuestionAsked `json:"question"`
}

type resumedPayload struct {
	QuestionID string           `json:"question_id"`
	Answer     contracts.Answer `json:"answer"`
}

// ParkLog is the waiting-on-answer durable state: Park/Resume append
// TIParked/TIResumed items to the thread's own log on any
// contracts.ThreadStore (internal/persistence's LocalStore for real
// cross-restart durability, MemStore for tests/ephemeral pods); IsWaiting
// reconstructs current state by REPLAY. There is nothing to lose across a
// daemon restart beyond the store itself — no separate in-memory registry
// this package must also durably maintain.
//
// A cross-thread "queue" (the needs-jacinta inbox reading every parked
// thread across the daemon) is NOT built here: that index is a seam for
// the io/remote unit that owns the inbox surface (agora-spec-io.md §7) —
// this package gives it IsWaiting per-thread and the TIParked/TIResumed
// item shapes to scan for.
// Spec: agora-spec-planning-questions.md §5 (ladder, interactive row), §6.
type ParkLog struct {
	store contracts.ThreadStore
}

// NewParkLog builds a ParkLog over store.
func NewParkLog(store contracts.ThreadStore) *ParkLog {
	return &ParkLog{store: store}
}

// Park records the thread as waiting-on-answer for q.
// Spec §5: "thread → waiting-on-answer (durable, survives daemon restart +
// detach), question becomes a queued card".
func (l *ParkLog) Park(threadID string, q contracts.QuestionAsked, ts time.Time, identity string) error {
	if err := l.store.Append(threadID, []contracts.ThreadItem{{
		TS: ts, Type: contracts.TIParked, Identity: identity,
		Payload: parkedPayload{Question: q},
	}}); err != nil {
		return fmt.Errorf("planning: park: %w", err)
	}
	return nil
}

// Resume records the answer and un-parks the thread (appends TIResumed).
// Returns ErrNotWaiting if the thread has no matching open park record — a
// stray/late answer for a question that never parked, or one that already
// resumed. The answer itself (ans) must already be attributed; Resume does
// not stamp By (that boundary is QuestionLog.Answer's job, one layer up).
func (l *ParkLog) Resume(threadID, questionID string, ans contracts.Answer, ts time.Time, identity string) error {
	waiting, ok, err := l.IsWaiting(threadID)
	if err != nil {
		return err
	}
	if !ok || waiting.Question.ID != questionID {
		return fmt.Errorf("%w: thread=%s question=%s", ErrNotWaiting, threadID, questionID)
	}
	if err := l.store.Append(threadID, []contracts.ThreadItem{{
		TS: ts, Type: contracts.TIResumed, Identity: identity,
		Payload: resumedPayload{QuestionID: questionID, Answer: ans},
	}}); err != nil {
		return fmt.Errorf("planning: resume: %w", err)
	}
	return nil
}

// IsWaiting replays the thread and reports its current park state: whether
// it is currently waiting on an answer, and if so, on which question. A
// thread may park and resume repeatedly across its life (multiple
// sequential blocking questions); only the LATEST unresolved park counts.
func (l *ParkLog) IsWaiting(threadID string) (WaitingState, bool, error) {
	it, err := l.store.Resume(threadID)
	if err != nil {
		return WaitingState{}, false, fmt.Errorf("planning: resume for park log: %w", err)
	}
	defer it.Close()

	var open *WaitingState
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		switch item.Type {
		case contracts.TIParked:
			p, decErr := decodeParked(item.Payload)
			if decErr != nil {
				return WaitingState{}, false, fmt.Errorf("planning: decode parked item seq %d: %w", item.Seq, decErr)
			}
			open = &WaitingState{ThreadID: threadID, Question: p.Question, ParkedAt: item.TS}
		case contracts.TIResumed:
			r, decErr := decodeResumed(item.Payload)
			if decErr != nil {
				return WaitingState{}, false, fmt.Errorf("planning: decode resumed item seq %d: %w", item.Seq, decErr)
			}
			if open != nil && open.Question.ID == r.QuestionID {
				open = nil
			}
		}
	}
	if err := it.Err(); err != nil {
		return WaitingState{}, false, fmt.Errorf("planning: replay park log: %w", err)
	}
	if open == nil {
		return WaitingState{}, false, nil
	}
	return *open, true, nil
}

func decodeParked(payload any) (parkedPayload, error) {
	var p parkedPayload
	b, err := json.Marshal(payload)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

func decodeResumed(payload any) (resumedPayload, error) {
	var r resumedPayload
	b, err := json.Marshal(payload)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	return r, err
}
