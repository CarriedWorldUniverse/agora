package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// itemPayload is the payload shape item.* events carry for agent_message/
// reasoning — matches internal/io/pipe.go's itemPayload shape byte-for-byte
// (that type is unexported to internal/io, so this is a local copy of the
// wire shape, not a reused Go type; contracts/testdata/flows/turn.jsonl is
// the frozen fixture both packages independently target).
type itemPayload struct {
	Text string `json:"text"`
}

// usagePayload is the turn.completed event body — matches
// conformance/helpers.go's usagePayload shape (same reuse-the-SHAPE note).
type usagePayload struct {
	Usage contracts.Usage `json:"usage"`
}

// turnFailedPayload is the turn.failed event body — matches
// internal/io/pipe.go's turnFailedPayload shape: {"interrupted": bool} is
// the ENGINE's authoritative signal that a failure was an interruption
// rather than a genuine error (see pipe.go's doc comment on why this can't
// be inferred client-side).
type turnFailedPayload struct {
	Interrupted bool `json:"interrupted"`
}

// errorPayload is the payload shape carried by an EvError event — matches
// internal/io/scripted_engine.go's errorPayload shape.
type errorPayload struct {
	Message string `json:"message"`
}

// turnSink implements bridle.EventSink for one in-flight turn: it
// translates bridle's Event vocabulary into agora contracts.Event and
// writes them to out, minting item.seq deterministically (a single
// monotonic per-turn counter, shared across item kinds — matches
// contracts/testdata/flows/turn.jsonl's single agent_message item at
// seq=1).
//
// This slice's translated subset (agora-engine-blueprint.md §5's TUI event
// mapping, narrowed to what a text-only turn against bridle's fake
// provider exercises):
//
//	ModelChunk    -> item.started (first chunk) + item.agent_message.delta
//	ReasoningChunk -> item.started (first chunk) + item.updated (each chunk)
//	TurnDone      -> item.completed for whichever item(s) are open (full
//	                 text/reasoning content) — turn.completed itself is
//	                 emitted by Manager from the RETURNED TurnResult, not
//	                 from this event, because bridle's own abort path
//	                 (run.go partialAbort*) returns StopReasonAborted
//	                 WITHOUT ever emitting TurnDone — so TurnDone is not a
//	                 reliable turn-boundary signal on its own.
//	TurnError/Warning -> error{message}
//	ToolCallStart/ToolCallResult -> // U-C5: tool-call item translation
//	                 (item.started/completed{command_execution}) is a
//	                 later ticket; the fake provider used by this slice's
//	                 tests never emits these unless scripted with
//	                 ToolCalls, which no test here does.
//	StepBoundary/MCPServerFailed -> no agora wire equivalent this slice;
//	                 dropped (documented, not silently forgotten: neither
//	                 has a contracts.EventType to land on yet).
//
// Note on the interrupted case: an aborted turn never reaches emitDone
// (bridle's abort path returns without emitting TurnDone — see the
// TurnDone bullet above), so an item.started with no matching
// item.completed is a normal, expected artifact of interruption, not a
// bug — there is no item-cancelled event in the contracts vocabulary.
// turn.failed{interrupted:true} (emitted by Manager, not this sink) is
// the terminal signal a consumer should key off of; it does not imply
// every open item was cleanly closed out.
type turnSink struct {
	threadID string
	turnID   string
	out      chan<- contracts.Event
	ctx      context.Context // gates delivery; the OUTER Run-level ctx, not the interrupt-scoped turn ctx — see manager.go's runOneTurn doc comment for why.

	mu       sync.Mutex
	seq      int64
	agentSeq int64 // 0 = agent_message item not yet started
	agentBuf strings.Builder
	reasSeq  int64 // 0 = reasoning item not yet started
	reasBuf  strings.Builder
}

func newTurnSink(threadID, turnID string, out chan<- contracts.Event, ctx context.Context) *turnSink {
	return &turnSink{threadID: threadID, turnID: turnID, out: out, ctx: ctx}
}

var _ bridle.EventSink = (*turnSink)(nil)

// Emit implements bridle.EventSink.
func (s *turnSink) Emit(e bridle.Event) {
	switch ev := e.(type) {
	case bridle.ModelChunk:
		s.emitChunk(ev.Text, &s.agentSeq, &s.agentBuf, contracts.ItemAgentMessage, contracts.EvAgentMessageDelta)
	case bridle.ReasoningChunk:
		s.emitChunk(ev.Text, &s.reasSeq, &s.reasBuf, contracts.ItemReasoning, contracts.EvItemUpdated)
	case bridle.TurnDone:
		s.emitDone(ev)
	case bridle.TurnError:
		msg := ""
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		s.send(contracts.Event{Type: contracts.EvError, Payload: mustMarshal(errorPayload{Message: msg})})
	case bridle.Warning:
		s.send(contracts.Event{Type: contracts.EvError, Payload: mustMarshal(errorPayload{Message: ev.Kind + ": " + ev.Message})})
	default:
		// bridle.ToolCallStart/ToolCallResult/StepBoundary/MCPServerFailed:
		// see the type doc comment above — out of scope this slice.
	}
}

// emitChunk handles the shared ModelChunk/ReasoningChunk shape: mint an
// item.seq and emit item.started on the first chunk seen for that item
// kind, then emit deltaType (item.agent_message.delta for text,
// item.updated for reasoning — bridle has no dedicated reasoning-delta
// wire type yet) for every chunk including the first.
func (s *turnSink) emitChunk(text string, itemSeq *int64, buf *strings.Builder, itemType contracts.ItemType, deltaType contracts.EventType) {
	s.mu.Lock()
	first := *itemSeq == 0
	if first {
		s.seq++
		*itemSeq = s.seq
	}
	buf.WriteString(text)
	seq := *itemSeq
	s.mu.Unlock()

	if first {
		s.send(contracts.Event{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Seq: seq, Type: itemType}})
	}
	s.send(contracts.Event{
		Type:    deltaType,
		Item:    &contracts.ItemRef{Seq: seq, Type: itemType},
		Payload: mustMarshal(itemPayload{Text: text}),
	})
}

// emitDone closes out whichever item(s) this turn opened, with the full
// accumulated content (preferring bridle's own authoritative
// TurnResult.FinalText for the agent_message item when non-empty, since
// that's the harness's settled answer — see ProviderResult.FinalText's
// doc comment on draft-vs-final text; the accumulated buffer is the
// fallback for providers/tests that never populate FinalText).
func (s *turnSink) emitDone(ev bridle.TurnDone) {
	s.mu.Lock()
	agentSeq, reasSeq := s.agentSeq, s.reasSeq
	agentText, reasText := s.agentBuf.String(), s.reasBuf.String()
	s.mu.Unlock()

	if agentSeq != 0 {
		text := agentText
		if ev.Result.FinalText != "" {
			text = ev.Result.FinalText
		}
		s.send(contracts.Event{
			Type:    contracts.EvItemCompleted,
			Item:    &contracts.ItemRef{Seq: agentSeq, Type: contracts.ItemAgentMessage},
			Payload: mustMarshal(itemPayload{Text: text}),
		})
	}
	if reasSeq != 0 {
		s.send(contracts.Event{
			Type:    contracts.EvItemCompleted,
			Item:    &contracts.ItemRef{Seq: reasSeq, Type: contracts.ItemReasoning},
			Payload: mustMarshal(itemPayload{Text: reasText}),
		})
	}
}

// send stamps thread/turn ids and delivers ev to out, backing off if ctx
// (the OUTER Run-level ctx) is done — mirrors internal/io/scripted_engine.go's
// sendEvent helper.
func (s *turnSink) send(ev contracts.Event) {
	ev.ThreadID = s.threadID
	ev.TurnID = s.turnID
	select {
	case s.out <- ev:
	case <-s.ctx.Done():
	}
}

// mustMarshal marshals a locally-defined, well-typed payload struct. A
// failure here is a programmer error — mirrors internal/io's own
// mustMarshal convention (same rationale: panicking surfaces the bug
// immediately instead of silently emitting a malformed event).
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("turnengine: marshal payload: %v", err))
	}
	return b
}
