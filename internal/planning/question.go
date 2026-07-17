package planning

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
}

// NewQuestionLog builds a QuestionLog over store.
func NewQuestionLog(store contracts.ThreadStore) *QuestionLog {
	return &QuestionLog{store: store, park: NewParkLog(store)}
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

	if err := l.store.Append(threadID, []contracts.ThreadItem{{
		TS: ts, Type: contracts.TIQuestionAnswered, Identity: identity,
		Payload: questionAnsweredPayload{QuestionID: questionID, Answer: ans},
	}}); err != nil {
		return fmt.Errorf("planning: append question_answered: %w", err)
	}

	waiting, ok, err := l.park.IsWaiting(threadID)
	if err != nil {
		return err
	}
	if ok && waiting.Question.ID == questionID {
		if err := l.park.Resume(threadID, questionID, ans, ts, identity); err != nil {
			return err
		}
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
