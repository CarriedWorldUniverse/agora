package opclient

import (
	"encoding/json"
	"time"
)

// observeFramePayload is the broker's observe.frame push payload
// (nexus/frames.ObserveFramePayload).
type observeFramePayload struct {
	Aspect string          `json:"aspect"`
	Frame  json.RawMessage `json:"frame"`
}

// observeFrame mirrors nexus/observability.Frame — the per-aspect frame
// stream multiplexed over observe.frame pushes.
type observeFrame struct {
	Kind     string          `json:"kind"` // "turn" | "chat" | "presence" | "filter_decision"
	Aspect   string          `json:"aspect"`
	Sequence int64           `json:"seq"`
	Payload  json.RawMessage `json:"payload"`
}

// TurnFrame is a complete snapshot of one deliberation turn, re-emitted on
// every change; renderers replace by TurnID rather than appending.
type TurnFrame struct {
	TurnID     string      `json:"turn_id"`
	Label      string      `json:"label,omitempty"` // "main" (default when empty) | "compact" | "filter-judge"
	Status     string      `json:"status"`          // "in_flight" | "complete" | "errored"
	Started    time.Time   `json:"started"`
	Ended      *time.Time  `json:"ended,omitempty"`
	TriggerMsg int64       `json:"trigger_msg,omitempty"`
	Events     []TurnEvent `json:"events"`
	Error      string      `json:"error,omitempty"`
}

// TurnEvent is one entry in a turn snapshot.
type TurnEvent struct {
	Kind string    `json:"kind"` // "text" | "tool_call" | "tool_result_orphan" | "step"
	Text string    `json:"text,omitempty"`
	Tool *ToolCall `json:"tool,omitempty"`
	Step int       `json:"step,omitempty"`
}

// ToolCall is one tool invocation inside a turn, with its result preview
// once available.
type ToolCall struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	Result *struct {
		Preview string `json:"preview"`
		IsError bool   `json:"is_error"`
	} `json:"result,omitempty"`
}

// ObserveTurn is emitted for every observe.frame push of kind "turn":
// a full snapshot of one deliberation turn (replace-by-TurnID).
type ObserveTurn struct {
	Aspect string
	Seq    int64
	Turn   TurnFrame
}

func (ObserveTurn) event() {}
