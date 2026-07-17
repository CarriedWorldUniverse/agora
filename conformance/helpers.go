package conformance

import (
	"encoding/json"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// mustFlowEvent marshals a locally-defined payload struct into an Event —
// a programmer error (a payload this test package controls failing to
// marshal) panics immediately rather than producing a malformed stream,
// mirroring internal/io's own mustMarshal convention.
func mustFlowEvent(t contracts.EventType, v any) contracts.Event {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("conformance: marshal payload: %v", err))
	}
	return contracts.Event{Type: t, Payload: b}
}

func mustMarshalJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("conformance: marshal: %v", err))
	}
	return b
}

// threadStartedPayload is the thread.started event body shared by every
// flow fixture (contracts/testdata/flows/*.jsonl) — never formalized as a
// contracts type (thread.started's payload is daemon-authored, not
// exchanged with a seam package), so the drives share one local definition
// rather than each hand-rolling the field order.
type threadStartedPayload struct {
	IdentityFP string `json:"identity_fp"`
	Profile    string `json:"profile"`
	WorkingDir string `json:"working_dir"`
}

// usagePayload is the turn.completed event body (contracts.Usage is
// required per bridle §2 / contracts_test.go's TestTurnCompletedCarriesUsage).
type usagePayload struct {
	Usage contracts.Usage `json:"usage"`
}

func newThreadStarted(threadID string, p threadStartedPayload) contracts.Event {
	return contracts.Event{Type: contracts.EvThreadStarted, ThreadID: threadID, Payload: mustMarshalJSON(p)}
}

func newTurnStarted(threadID, turnID string) contracts.Event {
	return contracts.Event{Type: contracts.EvTurnStarted, ThreadID: threadID, TurnID: turnID}
}

func newTurnCompleted(threadID, turnID string, u contracts.Usage) contracts.Event {
	return contracts.Event{Type: contracts.EvTurnCompleted, ThreadID: threadID, TurnID: turnID, Payload: mustMarshalJSON(usagePayload{Usage: u})}
}

// itemRefsEqual compares two *contracts.ItemRef by value (nil-safe) — the
// structural-comparison teeth finding #6(a) wants: a wrong Item.Seq/Type
// (e.g. both events being item.started with nil payloads) must fail a
// structural comparison, not silently pass because only Type/ThreadID were
// checked.
func itemRefsEqual(a, b *contracts.ItemRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
