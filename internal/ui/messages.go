// All tea.Msg types consumed by the Model. Pulled out of model.go
// so the message contract is scannable in one file. Pure type
// declarations, no behaviour.
package ui

import "time"

type InboxUpdated struct{}

type ChatDelivered struct {
	From       string
	Content    string
	MsgID      int64
	ReceivedAt time.Time
}

type ChatSent struct {
	To   string
	Body string
}

type ChatPanelReply struct {
	Body string
}

type EngineError struct {
	Source string
	Error  string
}

type NotifyOperator struct {
	Body string
}

type ModelChunk struct {
	Text string
}

type ModelTurnEnd struct{}

type ReadyToQuit struct{}

type RegisterSubmit struct {
	OnSubmit func(text string)
	InboxLen func() int
}

type wsTick struct{}

const wsTickInterval = 1500 * time.Millisecond
