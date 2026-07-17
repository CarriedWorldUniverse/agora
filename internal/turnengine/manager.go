package turnengine

import (
	"context"
	"errors"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// placeholderModel is the provisional TurnRequest.Model this slice
// hardcodes. TurnRequest.Model is REQUIRED (bridle.ErrModelRequired
// otherwise) but real per-thread/per-profile model selection is U-C4's
// job (BeforeModelCall wiring: tools, system prompt, model/provider from
// profile config) and bridle's own LaneClaudeSDK ModelInfo catalog row is
// itself a placeholder today (agora-engine-blueprint.md's U-A3 note).
// Overridable via WithModel for callers/tests that need a specific value.
const placeholderModel = "claude-placeholder"

// Manager implements agora's io.Engine (internal/io/engine.go) over a
// *bridle.Harness: it drives ONE deliberation turn per user_message input
// and streams the result as contracts.Event.
//
// Scope (Phase 2 U-C1 — the architecture-proving slice, NOT the finished
// adapter): no tool surface (Tools always empty, ToolRunner is a
// noop-that-errors-loudly), no approvals, no ctxmap/context assembly
// (AppendSystemPrompt is caller-supplied verbatim, no per-turn ThreadItem
// replay), no persistence (nothing survives past one Run call), no
// in-process launch wiring. One turn in flight at a time — a
// user_message arriving while a turn is already running is dropped
// (steering/queuing a second turn is out of scope; Session's
// first-answer-wins arbitration one layer up is the only de-duplication
// this unit relies on, per contracts/event.go and internal/io/session.go's
// existing design). Those are ALL later build units (U-C2..U-C7, U-D*,
// U-E*) — see doc.go and agora-engine-blueprint.md's build decomposition.
type Manager struct {
	threadID string
	provider bridle.Provider
	harness  *bridle.Harness

	model              string
	appendSystemPrompt string
	maxSteps           int
	idGen              IDGen
}

var _ agoraio.Engine = (*Manager)(nil)

// Option configures a Manager at construction (mirrors internal/ctxmgr's
// Option pattern).
type Option func(*Manager)

// WithModel overrides placeholderModel.
func WithModel(model string) Option { return func(m *Manager) { m.model = model } }

// WithAppendSystemPrompt sets TurnRequest.AppendSystemPrompt for every
// turn this Manager runs. Empty (the default) means bridle's own base
// prompt is used unmodified.
func WithAppendSystemPrompt(s string) Option { return func(m *Manager) { m.appendSystemPrompt = s } }

// WithMaxSteps sets TurnRequest.MaxSteps (0 = unlimited, bridle's
// default). See agora-engine-blueprint.md's "open items carried": a real
// profile-driven default belongs to a later unit; this slice's zero value
// is fine because its tests only ever drive a single-round text turn.
func WithMaxSteps(n int) Option { return func(m *Manager) { m.maxSteps = n } }

// WithIDGen overrides the default SeqIDGen — tests use this for
// deterministic turn ids.
func WithIDGen(g IDGen) Option { return func(m *Manager) { m.idGen = g } }

// NewManager builds a Manager for one thread over provider. provider is
// the injection seam: production callers pass provider/claudesdk.New()
// (funnel mode, per the blueprint's locked decision #2/#3); tests pass
// bridle/fake.NewProvider(steps...) — NEVER the real sidecar. One
// *bridle.Harness is built once, over provider, and reused for every turn
// this Manager's Run call drives (mirrors internal/ctxmgr's "one Manager
// per thread" lifecycle, per NewManager's brief-cited mirror target).
func NewManager(threadID string, provider bridle.Provider, opts ...Option) *Manager {
	m := &Manager{
		threadID: threadID,
		provider: provider,
		harness:  bridle.NewHarness(provider),
		model:    placeholderModel,
		idGen:    &SeqIDGen{},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Run implements agora io.Engine. It owns one thread end-to-end: a reader
// loop consumes Input from in until in closes or ctx is canceled, runs at
// most one turn at a time on ITS OWN goroutine (so interrupt/end can still
// be serviced while Harness.RunTurn blocks), and emits Event to out. Run
// closes out before returning, satisfying the io.Engine contract
// (internal/io/engine.go's doc comment; mirrored from
// internal/io/scripted_engine.go's Run).
func (m *Manager) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)

	var turnCancel context.CancelFunc
	var turnDone chan struct{}

	stopInFlight := func() {
		if turnCancel != nil {
			turnCancel()
			<-turnDone
			turnCancel, turnDone = nil, nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopInFlight()
			return ctx.Err()

		case <-turnDone:
			// The in-flight turn's goroutine finished on its own (no
			// interrupt/end involved) — clear the bookkeeping so the
			// next user_message can start a new turn. A nil turnDone
			// makes this case permanently non-firing until a turn is
			// started again (a nil channel in a select never fires).
			turnCancel, turnDone = nil, nil

		case input, ok := <-in:
			if !ok {
				stopInFlight()
				return nil
			}
			switch input.Type {
			case contracts.InUserMessage:
				if turnCancel != nil {
					// A turn is already in flight. Queuing/steering a
					// second turn is out of scope for this slice
					// (later: approval_response/question_response resume
					// the SAME turn per the blueprint's TUI event
					// mapping notes; a brand new user_message mid-turn
					// is a UI-level concern this unit doesn't model).
					continue
				}
				turnCtx, cancel := context.WithCancel(ctx)
				turnCancel = cancel
				done := make(chan struct{})
				turnDone = done
				go m.runOneTurn(ctx, turnCtx, input, out, done)

			case contracts.InInterrupt:
				if turnCancel != nil {
					turnCancel()
				}

			case contracts.InEnd:
				stopInFlight()
				return nil

			default:
				// steer/approval_response/question_response/config/
				// provision: no tool loop, no approvals, no context
				// assembly this slice — nothing to resume/apply yet.
			}
		}
	}
}

// runOneTurn drives exactly one bridle.Harness.RunTurn call and maps its
// outcome onto the agora event stream. sendCtx gates event DELIVERY (the
// outer Run-level ctx — see turnSink's doc comment: it must NOT be the
// interrupt-scoped turnCtx, because bridle's own abort path returns
// StopReasonAborted from an already-canceled ctx, and this function still
// needs to deliver the resulting turn.failed{interrupted:true} event).
// turnCtx gates the Harness.RunTurn call itself and is what
// Manager.Run's InInterrupt case cancels.
func (m *Manager) runOneTurn(sendCtx, turnCtx context.Context, input contracts.Input, out chan<- contracts.Event, done chan<- struct{}) {
	defer close(done)

	turnID := m.idGen.NextTurnID()
	emit := func(ev contracts.Event) {
		ev.ThreadID = m.threadID
		ev.TurnID = turnID
		select {
		case out <- ev:
		case <-sendCtx.Done():
		}
	}

	emit(contracts.Event{Type: contracts.EvTurnStarted})

	sink := newTurnSink(m.threadID, turnID, out, sendCtx)
	req := bridle.TurnRequest{
		Provider:           m.provider.Name(),
		Model:              m.model,
		AppendSystemPrompt: m.appendSystemPrompt,
		UserMessage:        input.Text,
		MaxSteps:           m.maxSteps,
	}

	result, err := m.harness.RunTurn(turnCtx, req, noopToolRunner{}, sink)

	switch result.StopReason {
	case bridle.StopReasonModelDone, bridle.StopReasonMaxSteps:
		emit(contracts.Event{
			Type:    contracts.EvTurnCompleted,
			Payload: mustMarshal(usagePayload{Usage: mapUsage(result.Usage)}),
		})

	case bridle.StopReasonAborted:
		emit(contracts.Event{
			Type:    contracts.EvTurnFailed,
			Payload: mustMarshal(turnFailedPayload{Interrupted: true}),
		})

	default: // Error, Refusal, ProcessExit, or an unset/unknown value.
		// A provider-round failure already went through sink.Emit
		// (bridle's run.go: sink.Emit(TurnError{...}) before returning
		// err) and was translated to an `error` event by turnSink. The
		// one path that bypasses the sink entirely is RunTurn's own
		// preflight check (empty req.Model) — surface that explicitly
		// so an empty-model bug doesn't fail silently on the wire.
		if errors.Is(err, bridle.ErrModelRequired) {
			emit(contracts.Event{Type: contracts.EvError, Payload: mustMarshal(errorPayload{Message: err.Error()})})
		}
		emit(contracts.Event{
			Type:    contracts.EvTurnFailed,
			Payload: mustMarshal(turnFailedPayload{Interrupted: false}),
		})
	}
}

// mapUsage translates bridle.Usage (per-provider accounting detail) onto
// contracts.Usage (the wire shape, agora-spec-bridle §2). CacheReadInputTokens
// maps to Cached — the discounted-rate re-read count is the field
// contracts.Usage.Cached documents; CacheCreationInputTokens (tokens newly
// WRITTEN into the cache) has no wire equivalent yet and is dropped rather
// than double-counted into either Input or Cached.
func mapUsage(u bridle.Usage) contracts.Usage {
	return contracts.Usage{
		Input:     int64(u.InputTokens),
		Output:    int64(u.OutputTokens),
		Cached:    int64(u.CacheReadInputTokens),
		Reasoning: int64(u.ReasoningTokens),
	}
}
