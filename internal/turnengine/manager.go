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
// internal/io/scripted_engine.go's Run). Note this is only half the
// contract: Run guarantees it WILL close out, but relies on the consumer
// (RunPipe, Session, or a test) draining out until that close is observed —
// a consumer that stops reading early can make Run's own sends block
// forever against a full, unread channel.
func (m *Manager) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)

	var turnCancel context.CancelFunc
	// turnDone carries the in-flight turn's TERMINAL event (turn.completed
	// or turn.failed), not just a bare "done" signal. runOneTurn computes
	// that event but does NOT send it to out itself — it hands it to Run
	// via this channel, and Run is the one that forwards it to out, in the
	// SAME select-case step that resets turnCancel/turnDone. This makes
	// "the terminal event becomes visible on out" and "bookkeeping says
	// the turn is over" a single atomic action from Run's single-threaded
	// perspective: no InUserMessage arriving on `in` can ever land in the
	// gap between them, because there IS no gap — earlier revisions had
	// runOneTurn emit the terminal event to out directly and separately
	// close a bare struct{} channel, which left exactly that gap (the few
	// instructions between "the emit's send lands" and "the deferred
	// close(done) actually runs" on a DIFFERENT goroutine); an
	// adversarial stress test (10k+ rapid turn-completion/next-message
	// cycles under -race) reproduced it at roughly 1-2 per 10000 attempts
	// — rare enough to look "fixed" against a modest iteration count, but
	// a real, reproducible silent message drop, not scheduler noise.
	var turnDone chan contracts.Event

	forward := func(ev contracts.Event) {
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}

	stopInFlight := func() {
		if turnCancel != nil {
			turnCancel()
			forward(<-turnDone)
			turnCancel, turnDone = nil, nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopInFlight()
			return ctx.Err()

		case ev := <-turnDone:
			// The in-flight turn's goroutine finished on its own (no
			// interrupt/end involved): forward its terminal event and
			// cancel its now-finished turnCtx (cancelling an
			// already-finished context is documented-safe; NOT calling
			// it here would leak that context.WithCancel's child
			// registration into ctx's children set for as long as this
			// Manager runs — one leak per completed turn, over a whole
			// thread's lifetime, since a Manager is reused across every
			// turn on a thread) in the SAME step, so no other case of
			// this select can interleave between "event visible" and
			// "bookkeeping reset". A nil turnDone makes this case
			// permanently non-firing until a turn is started again (a
			// nil channel in a select never fires).
			forward(ev)
			turnCancel()
			turnCancel, turnDone = nil, nil

		case input, ok := <-in:
			if !ok {
				stopInFlight()
				return nil
			}
			switch input.Type {
			case contracts.InUserMessage:
				if turnDone != nil {
					// A finished turn's terminal event may already be
					// sitting ready alongside this very user_message in
					// Go's pseudo-random select (both this case and the
					// case above can be simultaneously ready when a turn
					// completes right before the next message arrives)
					// — if `in` happens to win that draw, reap the
					// finished turn HERE (forward its event, same as
					// the case above) before deciding whether this is a
					// genuine mid-turn message. Non-blocking: if the
					// turn is still genuinely running, this default-cases
					// out and falls through to the mid-turn check below
					// unchanged.
					select {
					case ev := <-turnDone:
						forward(ev)
						turnCancel()
						turnCancel, turnDone = nil, nil
					default:
					}
				}
				if turnCancel != nil {
					// A turn is GENUINELY still in flight. Queuing/
					// steering a second turn is out of scope for this
					// slice (later: approval_response/question_response
					// resume the SAME turn per the blueprint's TUI event
					// mapping notes; a brand new user_message mid-turn
					// is a UI-level concern this unit doesn't model).
					continue
				}
				turnCtx, cancel := context.WithCancel(ctx)
				turnCancel = cancel
				done := make(chan contracts.Event)
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
// outcome onto the agora event stream. sendCtx gates event DELIVERY for
// every NON-terminal event this function emits directly (turn.started,
// plus everything the sink writes) — the outer Run-level ctx, not the
// interrupt-scoped turnCtx, because bridle's own abort path returns
// StopReasonAborted from an already-canceled ctx and this function still
// needs those earlier events to have had a chance to deliver. turnCtx
// gates the Harness.RunTurn call itself and is what Manager.Run's
// InInterrupt case cancels.
//
// The TERMINAL event (turn.completed/turn.failed) is different: it is not
// sent to out here at all. It's handed to done, and Manager.Run is the one
// that forwards it to out — see Run's doc comment on turnDone for why
// (closing the TOCTOU gap a stress test found between "terminal event
// visible" and "bookkeeping says the turn is over"). done is unbuffered
// and always has exactly one send on every path through this function;
// Run always eventually receives it (stopInFlight/the turnDone case/the
// InUserMessage reap all drain it), so this never leaks a blocked
// goroutine.
func (m *Manager) runOneTurn(sendCtx, turnCtx context.Context, input contracts.Input, out chan<- contracts.Event, done chan<- contracts.Event) {
	turnID := m.idGen.NextTurnID()
	emit := func(ev contracts.Event) {
		ev.ThreadID = m.threadID
		ev.TurnID = turnID
		select {
		case out <- ev:
		case <-sendCtx.Done():
		}
	}
	terminal := func(ev contracts.Event) {
		ev.ThreadID = m.threadID
		ev.TurnID = turnID
		done <- ev
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
		terminal(contracts.Event{
			Type:    contracts.EvTurnCompleted,
			Payload: mustMarshal(usagePayload{Usage: mapUsage(result.Usage)}),
		})

	case bridle.StopReasonAborted:
		terminal(contracts.Event{
			Type:    contracts.EvTurnFailed,
			Payload: mustMarshal(turnFailedPayload{Interrupted: true}),
		})

	default: // Error, Refusal, ProcessExit, or an unset/unknown value.
		// A provider-round failure already went through sink.Emit
		// (bridle's run.go: sink.Emit(TurnError{...}) before returning
		// err) and was translated to an `error` event by turnSink. The
		// one path that bypasses the sink entirely is RunTurn's own
		// preflight check (empty req.Model) — surface that explicitly
		// so an empty-model bug doesn't fail silently on the wire. This
		// is a non-terminal event so it goes through emit (out), not
		// terminal (done) — the actual terminal turn.failed follows it.
		if errors.Is(err, bridle.ErrModelRequired) {
			emit(contracts.Event{Type: contracts.EvError, Payload: mustMarshal(errorPayload{Message: err.Error()})})
		}
		terminal(contracts.Event{
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
