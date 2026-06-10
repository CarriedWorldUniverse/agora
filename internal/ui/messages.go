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

type ClearStatusNotice struct{}

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

// sendEchoTimeout fires echoAckTimeout after a chat.send; if the send's
// broker echo hasn't reconciled the pending block by then, the block is
// marked undelivered.
type sendEchoTimeout struct{ seq int64 }

// presenceTick drives the 1s elapsed re-render while the agent's
// presence is active. The chain self-terminates when presence clears.
type presenceTick struct{}

const idleTickInterval = 60 * time.Second
const idleThreshold = 5 * time.Minute

// echoAckTimeout bounds how long a sent message may wait for its
// chat.deliver echo before rendering ✗ undelivered.
const echoAckTimeout = 10 * time.Second

// presenceStaleAfter is the staleness guard on observe-driven presence:
// an in-flight turn with no fresh snapshot for this long stops counting.
const presenceStaleAfter = 5 * time.Minute
