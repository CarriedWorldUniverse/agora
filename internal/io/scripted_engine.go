package io

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// ScriptedTurn is one canned reaction: the Events a ScriptedEngine emits for
// the next Input it reads. Consumed strictly in order — the Nth Input read
// produces Script[N]'s Events, regardless of the Input's own content. This
// keeps the fixture the single source of truth for a golden test (the input
// text is whatever the test wants to log; the output is exactly the
// fixture), rather than making ScriptedEngine a pattern-matcher.
type ScriptedTurn struct {
	Events []contracts.Event
}

// ScriptedEngine is a deterministic, non-model stub Engine for tests: golden
// JSONL pipe fixtures and Session fan-out/replay/first-answer-wins tests all
// need a fully reproducible driver with no timing or model dependency.
//
// Spec-consistency note: the real Engine will react to approval_response /
// question_response by resuming the SAME in-flight turn rather than waiting
// for a fresh user_message. ScriptedEngine deliberately does not model that
// — it treats every Input as "advance to the next canned turn" — because
// modeling the real turn state machine is out of U2's scope (that belongs
// to the turn-engine unit). Session's first-answer-wins arbitration (which
// IS in scope here) happens one layer up, before an Input ever reaches
// Engine, so this simplification doesn't weaken what U2 needs to prove.
type ScriptedEngine struct {
	Script []ScriptedTurn
}

var _ Engine = (*ScriptedEngine)(nil)

// ErrUnscripted is emitted (as an EvError event, not a panic — a stub used
// outside a test harness should still produce a well-formed stream) when
// more Input arrives than the Script has turns for.
var ErrUnscripted = fmt.Errorf("scripted_engine: unscripted input")

func (e *ScriptedEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	i := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-in:
			if !ok {
				return nil
			}
			if i >= len(e.Script) {
				ev := contracts.Event{Type: contracts.EvError, Payload: mustMarshal(errorPayload{Message: ErrUnscripted.Error()})}
				if !sendEvent(ctx, out, ev) {
					return ctx.Err()
				}
				continue
			}
			turn := e.Script[i]
			i++
			for _, ev := range turn.Events {
				if !sendEvent(ctx, out, ev) {
					return ctx.Err()
				}
			}
		}
	}
}

func sendEvent(ctx context.Context, out chan<- contracts.Event, ev contracts.Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// errorPayload is the payload shape carried by an EvError event.
// Spec: agora-spec-io.md §1 (`error {message}`).
type errorPayload struct {
	Message string `json:"message"`
}

// mustMarshal marshals a locally-defined, well-typed payload struct. A
// failure here is a programmer error (an unmarshalable field on a type this
// package controls), never a runtime condition — panicking surfaces the bug
// immediately instead of silently emitting a malformed event.
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("io: marshal payload: %v", err))
	}
	return b
}
