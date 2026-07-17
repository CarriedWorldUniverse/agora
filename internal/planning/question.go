package planning

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// AskRequest is one harness-intrinsic `question` tool call, already
// harness-stamped by the engine layer: Source and Context are supplied by
// the caller (never the model — see contracts.QuestionAsked's doc comment
// on why Source is kept out of the model-facing contracts.QuestionArgs
// type), Args is the model's content.
// Spec: agora-spec-planning-questions.md §4.
type AskRequest struct {
	ThreadID string
	Args     contracts.QuestionArgs
	Source   contracts.QuestionSource
	Blocking bool
	Context  QuestionContext
	TS       time.Time
	// Identity is who/what raised the question — the audit trail on the
	// TIQuestionAsked item (distinct from Source, which is the question's
	// own provenance field on the wire).
	Identity string
}

// Outcome is what Ask produced: the minted question plus the escalation
// ladder's resolution. Exactly one of Parked / Terminate / Bubble is set,
// matching Disposition — DispositionQueue sets none of them: the question
// is filed (a TIQuestionAsked audit item) and the thread/job simply
// continues (§5).
type Outcome struct {
	Question    contracts.QuestionAsked
	Disposition Disposition
	// Parked is set for DispositionPark: the durable waiting-on-answer
	// record just created.
	Parked *WaitingState
	// Terminate is set for DispositionDieHonestly: the one-shot pod
	// termination shape the caller must actually terminate with.
	Terminate *contracts.BlockedNeedsInput
	// Bubble is set for DispositionBubble: the question the caller must
	// surface to the parent through its own result channel.
	Bubble *contracts.QuestionAsked
}

// QuestionLog ties the escalation ladder to durable thread state: every Ask
// appends a TIQuestionAsked audit item, then routes by Disposition — park
// persists the waiting-on-answer record (ParkLog); die-honestly and bubble
// persist nothing further (the caller owns actually terminating the pod /
// bubbling to the parent — that transport is another unit's job, §7/§8);
// queue persists nothing beyond the Asked item.
type QuestionLog struct {
	store contracts.ThreadStore
	park  *ParkLog

	// mu guards locks; each thread gets its own mutex so Ask/Answer's
	// check-then-act (IsWaiting -> append -> Park/Resume) is atomic PER
	// THREAD — two concurrent answers to the same parked question cannot both
	// resume it, and a second Ask cannot park over an unresolved one (review
	// HIGH TOCTOU + MED park-over-park). A per-thread mutex (rather than one
	// global lock) keeps unrelated threads concurrent.
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewQuestionLog builds a QuestionLog over store.
func NewQuestionLog(store contracts.ThreadStore) *QuestionLog {
	return &QuestionLog{store: store, park: NewParkLog(store), locks: make(map[string]*sync.Mutex)}
}

// threadLock returns the per-thread serialization mutex, creating it on first
// use. Held across the whole Ask/Answer critical section.
func (l *QuestionLog) threadLock(threadID string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.locks[threadID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[threadID] = m
	}
	return m
}

// Ask raises a question: mints its ID, resolves the escalation ladder for
// (req.Context, req.Blocking), and persists a TIQuestionAsked audit item
// plus (for DispositionPark) the durable waiting-on-answer record.
func (l *QuestionLog) Ask(req AskRequest) (Outcome, error) {
	id, err := newQuestionID()
	if err != nil {
		return Outcome{}, err
	}
	q := contracts.QuestionAsked{ID: id, Source: req.Source, Blocking: req.Blocking, Args: req.Args}

	disp, err := Resolve(req.Context, req.Blocking)
	if err != nil {
		return Outcome{}, err
	}

	tl := l.threadLock(req.ThreadID)
	tl.Lock()
	defer tl.Unlock()

	if disp == DispositionPark {
		// One blocking question per thread (§4 "one thing at a time"): refuse
		// to park a second over an unresolved one — checked BEFORE appending
		// the audit item, so a rejected Ask leaves no partial state (review MED).
		if _, waiting, werr := l.park.IsWaiting(req.ThreadID); werr != nil {
			return Outcome{}, werr
		} else if waiting {
			return Outcome{}, ErrAlreadyParked
		}
	}

	if err := l.store.Append(req.ThreadID, []contracts.ThreadItem{{
		TS: req.TS, Type: contracts.TIQuestionAsked, Identity: req.Identity, Payload: q,
	}}); err != nil {
		return Outcome{}, fmt.Errorf("planning: append question_asked: %w", err)
	}

	out := Outcome{Question: q, Disposition: disp}
	switch disp {
	case DispositionPark:
		if err := l.park.Park(req.ThreadID, q, req.TS, req.Identity); err != nil {
			return Outcome{}, err
		}
		waiting, ok, err := l.park.IsWaiting(req.ThreadID)
		if err != nil {
			return Outcome{}, err
		}
		if !ok {
			return Outcome{}, fmt.Errorf("planning: park recorded but thread not waiting (invariant violated)")
		}
		out.Parked = &waiting

	case DispositionDieHonestly:
		bni := Terminate(q, req.ThreadID)
		out.Terminate = &bni

	case DispositionBubble:
		qq := q
		out.Bubble = &qq

	case DispositionQueue:
		// Nothing further: filed, thread/job continues (§5).
	}
	return out, nil
}

// questionAnsweredPayload is the TIQuestionAnswered item payload shape.
type questionAnsweredPayload struct {
	QuestionID string           `json:"question_id"`
	Answer     contracts.Answer `json:"answer"`
}

// Answer resolves a question with an attributed answer: appends
// TIQuestionAnswered, and — if the thread is currently parked on this
// question — un-parks it (TIResumed). ans.By must already be stamped by the
// caller from the authenticated connection identity (never copied from
// client input, contracts.Answer's doc comment); Answer refuses an
// unattributed one rather than silently accepting a forged/blank actor.
func (l *QuestionLog) Answer(threadID, questionID string, ans contracts.Answer, ts time.Time, identity string) error {
	if ans.By == "" {
		return ErrUnattributedAnswer
	}

	// Serialize the whole check-then-act per thread: two concurrent answers to
	// the same parked question must resume it exactly once, not twice (review
	// HIGH TOCTOU — the losing answer still records its audit item but does not
	// re-resume the already-un-parked thread).
	tl := l.threadLock(threadID)
	tl.Lock()
	defer tl.Unlock()

	waiting, ok, err := l.park.IsWaiting(threadID)
	if err != nil {
		return err
	}

	if err := l.store.Append(threadID, []contracts.ThreadItem{{
		TS: ts, Type: contracts.TIQuestionAnswered, Identity: identity,
		Payload: questionAnsweredPayload{QuestionID: questionID, Answer: ans},
	}}); err != nil {
		return fmt.Errorf("planning: append question_answered: %w", err)
	}

	if ok && waiting.Question.ID == questionID {
		return l.park.Resume(threadID, questionID, ans, ts, identity)
	}
	return nil
}

func newQuestionID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("planning: generate question id: %w", err)
	}
	return "q_" + hex.EncodeToString(b[:]), nil
}
