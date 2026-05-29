// All tea.Msg types consumed by the Model. Pulled out of model.go
// so the message contract is scannable in one file. Pure type
// declarations, no behaviour.
package ui

import (
	"encoding/json"
	"time"
)

type InboxUpdated struct{}

// EscalationRequestReceived is sent into the program when the broker
// pushes an escalation.request (a native-API aspect asking a human to
// approve/deny a tool call). The Model opens a prominent approval modal
// on receipt. RequestID is the correlation id the decision must echo
// back so the blocked aspect's Request resolves.
type EscalationRequestReceived struct {
	RequestID string
	Aspect    string
	Tool      string
	Args      json.RawMessage
	Reason    string
}

type NotifyOperator struct {
	Body string
}

// TurnStarted opens a new streaming block for the next turn.
// Emitted by AgoraReturnHandler.OnTurnStart.
type TurnStarted struct {
	Source string
	MsgID  int64
}

// TurnChunk appends one streamed token's worth of text to the active
// block. Replaces ModelChunk.
type TurnChunk struct {
	Text string
}

// TurnDone finalises the active streaming block. Replaces ModelTurnEnd.
//
// FinalText is the model's final assistant text with any
// notify-operator fences stripped (the cleaned reply). HadNotify is
// true when at least one notify-operator block was extracted from the
// raw reply. The engine populates both (it owns the stripping — the ui
// package can't import engine). When HadNotify is true the Model
// reconciles the inline streamed block to FinalText so the notify body
// doesn't render twice (inline AND as the red blockNotify). When false
// the streamed block is finalised as-is.
type TurnDone struct {
	FinalText string
	HadNotify bool
}

// TurnFailed marks the active block as failed; body content stays
// visible, header re-renders with a failure reason.
type TurnFailed struct {
	Reason string
}

// SubmissionDropped is sent by the engine OnDrop callback when a TTY
// submission is silently dropped by the 15-min content-hash dedupe.
// The UI renders it as a system block so the operator knows their
// input was received but suppressed rather than ignored.
type SubmissionDropped struct {
	Reason    string
	FirstSeen time.Time
}

type ReadyToQuit struct{}

type RegisterSubmit struct {
	OnSubmit func(text string)
	InboxLen func() int
	// OnEscalationDecision dispatches an operator escalation decision to
	// the bus. Optional: nil leaves the modal able to render but its
	// confirm becomes a no-op send (still clears the modal). Wired in
	// main.go to bus.SendEscalationDecision.
	OnEscalationDecision func(aspect, decision, note, requestID string) error
}

type wsTick struct{}

const wsTickInterval = 1500 * time.Millisecond

type idleTick struct{}

const idleTickInterval = 60 * time.Second
const idleThreshold = 5 * time.Minute
