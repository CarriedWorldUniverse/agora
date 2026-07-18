package turnengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
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
// Scope (Phase 2 U-C1 built the architecture-proving text-only slice;
// U-C2 wired in the Phase 1 tool surface: TurnRequest.Tools now carries
// the fs/exec specs and tool calls actually EXECUTE via
// surfaceRunner/toolrunner.Surface; U-C3 — this unit — GATES that
// execution: every call now goes through a BeforeToolCall hook
// (approval.go) resolved via internal/approval.Decide before it can run —
// see approval.go's package-level doc comments for the hook/rendezvous/
// scope design). STILL missing: real MCP wiring (TurnRequest.MCP stays
// unset — MCP tools ride in Tools, per the blueprint's claudesdk
// SupportsMCP=false note), ctxmap/context
// assembly (AppendSystemPrompt is caller-supplied verbatim, no per-turn
// ThreadItem replay), no persistence (nothing survives past one Run
// call), no in-process launch wiring. One turn in flight at a time — a
// user_message arriving while a turn is already running is dropped
// (steering/queuing a second turn is out of scope; Session's
// first-answer-wins arbitration one layer up is the only de-duplication
// this unit relies on, per contracts/event.go and internal/io/session.go's
// existing design). Those are ALL later build units (U-C3..U-C7, U-D*,
// U-E*) — see doc.go and agora-engine-blueprint.md's build decomposition.
type Manager struct {
	threadID string
	provider bridle.Provider
	harness  *bridle.Harness
	surface  *toolrunner.Surface

	model              string
	appendSystemPrompt string
	maxSteps           int
	roots              toolrunner.Roots
	idGen              IDGen

	// U-C3: the BeforeToolCall approval gate. policy/scopeStore feed
	// approval.Decide (reused verbatim); hookMu/hookTurn and waiterMu/
	// waiters are the two pieces of cross-goroutine state the gate's
	// interactive rendezvous needs — see approval.go's doc comments for
	// why each is mutex-guarded and by which goroutines.
	policy     contracts.PolicySet
	scopeStore approval.ScopeStore

	hookMu   sync.Mutex
	hookTurn *turnHookCtx

	waiterMu sync.Mutex
	waiters  map[string]chan approvalOutcome
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

// WithRoots sets the writable-root set (agora-spec-io.md §3a) the fs/exec
// tool families are constrained to. Unset (the default, zero Roots{}) is
// resolved by NewManager into toolrunner.NewRoots(os.Getwd()) — the
// process's own current directory, no additional add_dirs. Real
// per-thread/per-profile root configuration (add_dir, a working dir other
// than the process cwd) is a later unit; this default is the only sane
// zero-config choice for a Manager built without one.
func WithRoots(roots toolrunner.Roots) Option { return func(m *Manager) { m.roots = roots } }

// NewManager builds a Manager for one thread over provider. provider is
// the injection seam: production callers pass provider/claudesdk.New()
// (funnel mode, per the blueprint's locked decision #2/#3); tests pass
// bridle/fake.NewProvider(steps...) — NEVER the real sidecar. One
// *bridle.Harness is built once, over provider, and reused for every turn
// this Manager's Run call drives (mirrors internal/ctxmgr's "one Manager
// per thread" lifecycle, per NewManager's brief-cited mirror target).
func NewManager(threadID string, provider bridle.Provider, opts ...Option) *Manager {
	m := &Manager{
		threadID:   threadID,
		provider:   provider,
		harness:    bridle.NewHarness(provider),
		model:      placeholderModel,
		idGen:      &SeqIDGen{},
		policy:     defaultPolicy(),
		scopeStore: approval.NewMemScopeStore(),
		waiters:    make(map[string]chan approvalOutcome),
	}
	for _, o := range opts {
		o(m)
	}
	if m.roots.WorkingDir == "" {
		m.roots = defaultRoots()
	}
	// mcp: nil — NewSurface documents nil as "no MCP servers configured/
	// folded in, Specs/Execute simply skip it", which is exactly this
	// unit's scope (real MCP wiring is a later ticket; TurnRequest.MCP
	// also stays unset in runOneTurn, per the blueprint's claudesdk
	// SupportsMCP=false note — MCP tools ride in Tools, not MCP).
	m.surface = toolrunner.NewSurface(nil, toolrunner.NewFSFamily(m.roots), toolrunner.NewExecFamily(m.roots))
	// U-C3: gate every tool call through the approval pipeline. Registered
	// ONCE here (bridle's Hook registration is documented not-safe to call
	// concurrently with RunTurn, matching this package's existing "wired
	// during setup" convention) and reused for every turn this Manager
	// drives — see approval.go's beforeToolCall doc comment.
	m.harness.RegisterBeforeToolCall(m.beforeToolCall)
	return m
}

// defaultRoots builds WithRoots's zero-value default: the process's own
// current working directory, canonicalized via toolrunner.NewRoots. Both
// os.Getwd and NewRoots (abs + symlink resolution) failing is an
// environment fault this package has no safe fallback for — silently
// defaulting to "." would resolve fs/exec tools against a directory that
// isn't actually where NewManager's caller thought it was, and swallowing
// the error into an unusable Roots{} would let every fs/exec call fail
// confusingly downstream instead of loudly at construction time. Panics,
// matching this package's existing mustMarshal "should never happen"
// convention (sink.go).
func defaultRoots() toolrunner.Roots {
	wd, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("turnengine: os.Getwd failed building the default tool Roots: %v", err))
	}
	roots, err := toolrunner.NewRoots(wd)
	if err != nil {
		panic(fmt.Sprintf("turnengine: toolrunner.NewRoots(%q) failed: %v", wd, err))
	}
	return roots
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
			m.setHookTurn(nil)
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
			m.setHookTurn(nil)

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
						m.setHookTurn(nil)
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
				// U-C3: Run — not runOneTurn — is the SOLE writer of
				// hookTurn (setHookTurn here, cleared at every reap point
				// above/below). This mints turnID here too, rather than
				// inside runOneTurn, so the hookTurn published for THIS
				// turn's BeforeToolCall hook always carries the SAME id
				// runOneTurn stamps onto its own events — no separate
				// "did the turn goroutine finish minting yet" question.
				//
				// Why not let runOneTurn set (and defer-clear) hookTurn on
				// its own goroutine, like the first cut of this unit did:
				// the turnDone channel handoff only orders the SEND on the
				// turn goroutine relative to the RECEIVE on Run's goroutine
				// — it says nothing about what either goroutine does
				// AFTER that. Turn A's deferred m.setHookTurn(nil) (running
				// on A's goroutine, after `done <- ev` completes) is NOT
				// ordered relative to turn B's m.setHookTurn(&newCtx)
				// (running on B's goroutine, spawned once Run reaps A's
				// terminal event) — back-to-back turns (a client sending
				// the next user_message the instant it sees turn.completed,
				// or this very loop's own InUserMessage opportunistic reap
				// above) can interleave A's clear AFTER B's set, silently
				// blinding B's BeforeToolCall hook (loadHookTurn returns
				// nil -> the defensive fail-closed-deny branch) for B's
				// entire turn. Reproduced under -race (see
				// TestManager_Approval_BackToBackTurns_NoHookTurnClobber).
				// This is the exact same bug CLASS as the turnDone TOCTOU
				// above (a cross-goroutine "before" that only looks ordered
				// because it usually IS, until the scheduler proves it
				// isn't) — same fix shape: move the write onto Run's own
				// single-threaded loop, in the SAME synchronized step as
				// the rest of that turn's bookkeeping reset/set.
				turnID := m.idGen.NextTurnID()
				m.setHookTurn(&turnHookCtx{threadID: m.threadID, turnID: turnID, out: out, sendCtx: ctx})
				go m.runOneTurn(ctx, turnCtx, turnID, input, out, done)

			case contracts.InInterrupt:
				if turnCancel != nil {
					turnCancel()
				}

			case contracts.InEnd:
				stopInFlight()
				return nil

			case contracts.InApprovalResponse:
				// Route straight to the waiter registry regardless of
				// whether a turn is "in flight" from Run's own
				// bookkeeping perspective: the ask rendezvous blocks the
				// TURN goroutine (inside RunTurn, mid-hook), which Run's
				// turnCancel/turnDone bookkeeping has no visibility into
				// — a pending approval is exactly the case where
				// turnCancel != nil but Run itself has nothing more to
				// do than forward this response to the goroutine that's
				// actually blocked. resolveWaiter no-ops on an unmatched
				// id (stale/duplicate/forged/already-resolved) — see its
				// doc comment in approval.go.
				m.resolveWaiter(input.ID, approvalOutcome{
					Decision: input.Decision,
					Scope:    input.Scope,
					Message:  input.Message,
				})

			default:
				// steer/question_response/config/provision: no tool
				// loop resume, no context assembly this slice — nothing
				// to apply yet.
			}
		}
	}
}

// runOneTurn drives exactly one bridle.Harness.RunTurn call and maps its
// outcome onto the agora event stream. turnID is minted by Run (not here)
// and passed in — see the InUserMessage case's doc comment on why Run must
// be the one to mint it and publish it via setHookTurn, ordered before this
// function's goroutine is even spawned. sendCtx gates event DELIVERY for
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
// goroutine. Run also clears hookTurn in that SAME reap step (see
// approval.go's setHookTurn doc comment) — this function does NOT touch
// hookTurn at all, unlike an earlier revision that set/deferred-cleared it
// on this goroutine (a cross-goroutine clobber the turnDone handoff does
// not order against the NEXT turn's set — see the InUserMessage doc
// comment above for the full trace).
func (m *Manager) runOneTurn(sendCtx, turnCtx context.Context, turnID string, input contracts.Input, out chan<- contracts.Event, done chan<- contracts.Event) {
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

	// Specs is fetched fresh per turn (not cached at Manager-construction
	// time) — the brief calls this out explicitly: Specs can be
	// ctx-dependent (a future MCPSource's Tools(ctx) may hit the network
	// or reflect servers that changed between turns), even though today's
	// fs/exec-only Surface is always static/in-memory (Specs' own doc:
	// "native family specs are always static/in-memory and cannot fail").
	// TurnRequest.MCP is deliberately left unset — MCP tools ride in
	// Tools, not MCP (blueprint: claudesdk SupportsMCP=false).
	toolSpecs, err := m.surface.Specs(turnCtx)
	if err != nil {
		terminal(contracts.Event{
			Type:    contracts.EvTurnFailed,
			Payload: mustMarshal(turnFailedPayload{Interrupted: false}),
		})
		return
	}
	req := bridle.TurnRequest{
		Provider:           m.provider.Name(),
		Model:              m.model,
		AppendSystemPrompt: m.appendSystemPrompt,
		UserMessage:        input.Text,
		MaxSteps:           m.maxSteps,
		Tools:              toolDefsFromSpecs(toolSpecs),
	}

	result, err := m.harness.RunTurn(turnCtx, req, newSurfaceRunner(m.surface), sink)

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
