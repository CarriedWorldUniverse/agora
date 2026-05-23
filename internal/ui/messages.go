// All tea.Msg types consumed by the Model. Pulled out of model.go
// so the message contract is scannable in one file. Pure type
// declarations, no behaviour.
package ui

import "time"

type InboxUpdated struct{}

type NotifyOperator struct {
	Body string
}

type ModelChunk struct {
	Text string
}

type ModelTurnEnd struct{}

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
type TurnDone struct{}

// TurnFailed marks the active block as failed; body content stays
// visible, header re-renders with a failure reason.
type TurnFailed struct {
	Reason string
}

type ReadyToQuit struct{}

type RegisterSubmit struct {
	OnSubmit func(text string)
	InboxLen func() int
}

type wsTick struct{}

const wsTickInterval = 1500 * time.Millisecond

type idleTick struct{}

const idleTickInterval = 60 * time.Second
const idleThreshold = 5 * time.Minute
