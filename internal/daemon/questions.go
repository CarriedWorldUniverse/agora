// questions.go: the daemon-layer wire-event builders for the two DERIVED
// question/park events neither planning.QuestionLog.Ask nor
// planning.ParkLog.Park/Resume emit themselves (blueprint §1: "Two DERIVED
// wire events the flow-engine emits ITSELF"). Mirrors internal/ctxmgr's
// events.go pattern (payload struct + New*Event constructor).
package daemon

import (
	"encoding/json"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// threadWaitingPayload is the thread.waiting event body.
// Spec: agora-spec-planning-questions.md §7 (question_park_resume.jsonl:4
// carries {"question_id":"qu_0001"}).
type threadWaitingPayload struct {
	QuestionID string `json:"question_id"`
}

// threadResumedPayload is the thread.resumed event body.
type threadResumedPayload struct {
	QuestionID string `json:"question_id"`
}

// NewThreadWaitingEvent builds the wire event for a thread that just parked
// on a blocking question (planning.QuestionLog.Ask returned
// DispositionPark).
func NewThreadWaitingEvent(threadID, questionID string) contracts.Event {
	b, _ := json.Marshal(threadWaitingPayload{QuestionID: questionID})
	return contracts.Event{Type: contracts.EvThreadWaiting, ThreadID: threadID, Payload: b}
}

// NewThreadResumedEvent builds the wire event for a thread that just
// un-parked (planning.QuestionLog.Answer resumed it).
func NewThreadResumedEvent(threadID, questionID string) contracts.Event {
	b, _ := json.Marshal(threadResumedPayload{QuestionID: questionID})
	return contracts.Event{Type: contracts.EvThreadResumed, ThreadID: threadID, Payload: b}
}
