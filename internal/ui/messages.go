// All tea.Msg types consumed by the Model. Pulled out of model.go
// so the message contract is scannable in one file. Pure type
// declarations, no behaviour.
package ui

import (
	"encoding/json"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
)

type InboxUpdated struct{}

type HistoryLoaded struct {
	Messages []opclient.ChatMessage
	Err      error
}

type OpEventReceived struct {
	Event opclient.Event
}

type SendFailed struct {
	Text string
	Err  error
}

type opEventPoll struct{}

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

type ReadyToQuit struct{}

type idleTick struct{}

const idleTickInterval = 60 * time.Second
const idleThreshold = 5 * time.Minute
