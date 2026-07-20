package turnengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	bridleadapter "github.com/CarriedWorldUniverse/bridle/ctxmap/adapter"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/memory"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/render"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/store"
)

// Manager implements agora's io.Engine (internal/io/engine.go) over a
// *bridle.Harness: it drives ONE deliberation turn per user_message input
// and streams the result as contracts.Event.
//
// Scope (Phase 2 U-C1 built the architecture-proving text-only slice;
// U-C2 wired in the Phase 1 tool surface: TurnRequest.Tools now carries
// the fs/exec specs and tool calls actually EXECUTE via
// surfaceRunner/toolrunner.Surface; U-C3 GATES that
// execution: every call now goes through a BeforeToolCall hook
// (approval.go) resolved via internal/approval.Decide before it can run —
// see approval.go's package-level doc comments for the hook/rendezvous/
// scope design; U-C6/U-C7 — this unit — add optional durability: a
// WithStore(contracts.ThreadStore) Option persists each turn's
// ThreadItems at the turn boundary and gives the claude-sdk lane a stable
// per-thread Session so continuations RESUME the Claude conversation
// instead of starting fresh each turn — see WithStore's and runOneTurn's
// doc comments). STILL missing: real MCP wiring (TurnRequest.MCP stays
// unset — MCP tools ride in Tools, per the blueprint's claudesdk
// SupportsMCP=false note), ctxmap/context
// assembly (AppendSystemPrompt is caller-supplied verbatim, no per-turn
// ThreadItem replay INTO the prompt — U-C6 only writes items OUT),
// approval-decision persistence (deferred — see persistTurn's doc
// comment), no in-process launch wiring. One turn in flight at a time — a
// user_message arriving while a turn is already running is dropped
// (steering/queuing a second turn is out of scope; Session's
// first-answer-wins arbitration one layer up is the only de-duplication
// this unit relies on, per contracts/event.go and internal/io/session.go's
// existing design). Those are ALL later build units (U-D*, U-E*) — see
// doc.go and agora-engine-blueprint.md's build decomposition.
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

	// U-C6/U-C7: optional durability. store is nil by default (see
	// WithStore's doc comment — nil means NO persistence, not "persist to
	// a throwaway MemStore"). sessionStarted is read and written ONLY
	// from runOneTurn (via turnSession), which the Manager.Run state
	// machine guarantees never runs concurrently with itself (one turn
	// goroutine at a time — see Run's doc comment); the turnDone channel
	// handoff between consecutive turn goroutines is a synchronization
	// point, so this needs no separate mutex.
	store contracts.ThreadStore

	// sessionStarted flips true after the FIRST runOneTurn call computes
	// its session-id bookkeeping (see turnSession's doc comment); every
	// LATER turn on this Manager gets Session.New=false unconditionally.
	// The ONE-TIME prior-items probe (m.store.Resume, only when store !=
	// nil) that decides the first turn's New flag runs exactly once,
	// gated by this same bool — never re-probed on later turns.
	sessionStarted bool

	// U-D1: the per-Manager ctxmap context engine (working-state only —
	// see attachContextEngine's doc comment for scope/degrade behavior).
	// ctxEngineEnabled defaults true (WithContextEngine(false) opts out);
	// eng/detach stay nil whenever construction is skipped OR fails — every
	// touch of eng in this package is guarded with `if m.eng != nil`, so a
	// Manager with no working context engine behaves exactly as it did
	// before this unit.
	ctxEngineEnabled bool
	eng              *memory.Engine
	detach           func()
}

var _ agoraio.Engine = (*Manager)(nil)

// Option configures a Manager at construction (mirrors internal/ctxmgr's
// Option pattern).
type Option func(*Manager)

// WithModel overrides the current profile's Model (DevProfile's, unless a
// WithProfile earlier in the Option list already changed it — see
// NewManager's doc comment on option-application order).
func WithModel(model string) Option { return func(m *Manager) { m.model = model } }

// WithProfile overrides the Manager's BASE ProfileConfig (DevProfile() by
// default — see NewManager) with cfg's Model/AppendSystemPrompt/Policy/
// ScopeStore, all four at once. Like every other Option, a WithProfile
// earlier in NewManager's opts list is itself overridable by a more
// specific WithModel/WithAppendSystemPrompt/WithPolicy/WithScopeStore
// later in the same list (Options apply in argument order — see
// NewManager's doc comment). A zero-value ScopeStore field (cfg.ScopeStore
// == nil) is applied as-is, not silently defaulted back to a fresh
// approval.NewMemScopeStore() — callers building a ProfileConfig by hand
// are expected to set every field they care about; NewManager's own
// DevProfile() base already guarantees a non-nil ScopeStore for the
// no-options case.
func WithProfile(cfg ProfileConfig) Option {
	return func(m *Manager) {
		m.model = cfg.Model
		m.appendSystemPrompt = cfg.AppendSystemPrompt
		m.policy = cfg.Policy
		m.scopeStore = cfg.ScopeStore
	}
}

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

// WithStore gives the Manager a contracts.ThreadStore for durability
// (U-C6/U-C7): each turn's ThreadItems are Appended at the turn boundary
// (see runOneTurn's persistTurn call), and the FIRST turn's Session.New
// flag is computed by probing the store for prior items on this thread
// (see runOneTurn's turnSession doc comment) instead of always assuming a
// fresh session.
//
// The default (no WithStore) is store == nil, meaning NO PERSISTENCE —
// every store touch in this package is guarded with `if m.store != nil`,
// so a Manager built without this Option behaves EXACTLY as it did before
// this unit (existing tests stay green unmodified). This is a deliberate
// choice NOT to default to internal/persistence.NewMemStore(): defaulting
// to an implicit, invisible in-memory store would silently change every
// existing no-store Manager's Session.New behavior (still fine, since a
// fresh MemStore never has prior items) but would also silently start
// discarding Append errors nobody asked to be able to occur — an explicit
// nil is the honest "this Manager doesn't persist" state, matching this
// package's existing opt-in Option pattern (WithPolicy/WithScopeStore
// etc. mirror the same "caller states their explicit choice" posture).
//
// The caller is responsible for the thread's Create/Meta lifecycle
// (contracts.ThreadStore.Create) — WithStore does not call Create; Append
// against a not-yet-created thread returns whatever error the store
// documents (MemStore/LocalStore: ErrNotFound), which persistTurn treats
// as any other best-effort Append failure (logged, not fatal to the
// turn — see persistTurn's doc comment).
func WithStore(store contracts.ThreadStore) Option {
	return func(m *Manager) { m.store = store }
}

// WithContextEngine turns the per-Manager ctxmap working-state engine
// on/off (U-D1). Default (no WithContextEngine option) is ON — NewManager
// seeds ctxEngineEnabled=true before opts run, matching this package's
// existing "fully-formed by default" posture (c.f. NewManager's Option-
// precedence doc comment) — the working-state block is the whole point of
// wiring ctxmap in, so a Manager built with zero options should carry it.
// WithContextEngine(false) opts a Manager fully out (e.g. a test that
// wants to assert on AppendSystemPrompt without ctxmap's block mixed in):
// attachContextEngine is never called, m.eng/m.detach stay nil, and every
// turn runs exactly as it did before this unit.
func WithContextEngine(enabled bool) Option {
	return func(m *Manager) { m.ctxEngineEnabled = enabled }
}

// NewManager builds a Manager for one thread over provider. provider is
// the injection seam: production callers pass provider/claudesdk.New()
// (funnel mode, per the blueprint's locked decision #2/#3); tests pass
// bridle/fake.NewProvider(steps...) — NEVER the real sidecar. One
// *bridle.Harness is built once, over provider, and reused for every turn
// this Manager's Run call drives (mirrors internal/ctxmgr's "one Manager
// per thread" lifecycle, per NewManager's brief-cited mirror target).
//
// Option precedence (U-C4): the Manager struct is first seeded from
// DevProfile() — model/appendSystemPrompt/policy/scopeStore all come from
// the dev profile BEFORE any opts run, so a Manager built with zero options
// is a fully-formed dev-profile Manager, not a half-configured one. opts
// then apply in argument order on top of that base: a WithProfile(cfg) in
// the list overwrites all four of those fields from cfg (same as the
// DevProfile seed would), and any WithModel/WithAppendSystemPrompt/
// WithPolicy/WithScopeStore in the list overwrites its own single field —
// so whichever of those runs LAST for a given field wins, most-specific
// (a single-field Option) or most-recent (repeated/ordered Options) always
// beats the profile-wide default it followed. Callers who want a custom
// profile with a couple of fields tweaked should list WithProfile(cfg)
// BEFORE the single-field overrides they want to keep.
func NewManager(threadID string, provider bridle.Provider, opts ...Option) *Manager {
	profile := DevProfile()
	m := &Manager{
		threadID:           threadID,
		provider:           provider,
		harness:            bridle.NewHarness(provider),
		model:              profile.Model,
		appendSystemPrompt: profile.AppendSystemPrompt,
		idGen:              &SeqIDGen{},
		policy:             profile.Policy,
		scopeStore:         profile.ScopeStore,
		waiters:            make(map[string]chan approvalOutcome),
		ctxEngineEnabled:   true, // U-D1: default ON for the dev profile — see WithContextEngine
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
	// U-D1: attach the ctxmap context engine AFTER the approval hook above
	// — bridle runs BeforeToolCall hooks in registration order (hooks.go's
	// runHooks), so m.beforeToolCall always sees every tool call FIRST.
	// See approval.go's beforeToolCall doc comment for why running second
	// is exactly what ctxmap's own hook needs (it only ever intercepts its
	// own recall/inspect/read_raw tools, via the Deny+Result pattern, and
	// is never itself a gate for agora's Surface tools).
	if m.ctxEngineEnabled {
		m.attachContextEngine()
	}
	return m
}

// attachContextEngine builds the per-Manager ctxmap working-state engine
// and attaches it to m.harness (U-D1). v1 scope, deliberately minimal:
//
//   - store.Open(":memory:") — an in-memory, per-Manager fact store. No
//     durable persistence across process restarts: ctxmap's durable-fact
//     extraction (Proposer/Embedder/PairJudge) is NOT wired here (all
//     three are passed nil to memory.New) — see the doc.go/blueprint's
//     "v1 = WORKING-STATE only" scope line. A future unit that wants
//     cross-restart facts swaps this for store.Open(a real path); nothing
//     else in this function changes.
//   - render.New(st) — opens epoch 1 over that (empty) store.
//   - memory.New(..., nil, nil, nil) + EnableWorkingState() — the
//     deterministic "files touched / last command / recent steps"
//     progress block, fed purely by ObserveTool (no model, no
//     extraction). recall/inspect are still served (memory.Engine.Tools()
//     always returns them), just over an always-empty durable-fact store
//     — a no-op the model will rarely bother calling, per ctxmap's own
//     Framing text, which frames memory as automatic rather than a save/
//     recall ceremony.
//
// Degrade-without-ctxmap, not fail-NewManager-loudly: a construction
// failure here (sqlite open error, disk/fd exhaustion) is an environment
// fault that has nothing to do with whether a turn can otherwise run —
// unlike defaultRoots' mustMarshal-style panic (an unusable Roots{} would
// make EVERY later fs/exec call fail confusingly), a Manager with no
// context engine still runs turns correctly; it just does not get the
// working-state block or recall/inspect tools this turn. Logged to
// stderr, m.eng/m.detach left nil (see NewManager's Manager doc comment on
// the nil-guard convention), matching this package's existing best-effort
// posture toward optional side-channel state (c.f. persistTurn's doc
// comment on WithStore failures).
func (m *Manager) attachContextEngine() {
	st, err := store.Open(":memory:")
	if err != nil {
		fmt.Fprintf(os.Stderr, "turnengine: ctxmap store.Open failed for thread %s: %v (running WITHOUT the context engine)\n", m.threadID, err)
		return
	}
	rend, err := render.New(st)
	if err != nil {
		st.Close()
		fmt.Fprintf(os.Stderr, "turnengine: ctxmap render.New failed for thread %s: %v (running WITHOUT the context engine)\n", m.threadID, err)
		return
	}
	eng := memory.New(memory.Config{SessionID: m.threadID}, st, rend, nil, nil, nil)
	eng.EnableWorkingState()
	m.eng = eng
	m.detach = bridleadapter.Attach(m.harness, eng)
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
	// U-D1: Run is this Manager's one thread-lifetime scope (NewManager's
	// harness/hooks are reused across every turn Run drives — see
	// NewManager's doc comment), so it's the natural place to tear the
	// context engine back down: detach unregisters its hooks from
	// m.harness (a no-op past this point anyway, since RunTurn is never
	// called again after Run returns), and eng.Close() drains/stops its
	// extraction worker goroutine. Both are nil-guarded — a Manager built
	// with WithContextEngine(false), or whose attachContextEngine failed
	// and degraded, has nothing to tear down here.
	if m.detach != nil {
		defer m.detach()
	}
	if m.eng != nil {
		defer m.eng.Close()
	}

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
	// Per-turn model override: a non-empty input.Model (the TUI's /model
	// switch and the %-override both set it) wins for THIS turn; otherwise the
	// Manager's configured model (DevProfile / WithModel) applies. Without this
	// the request always used m.model and input.Model was silently dropped — so
	// /model and % changed only the status row, never the actual turn model.
	model := m.model
	if input.Model != "" {
		model = input.Model
	}
	req := bridle.TurnRequest{
		Provider:           m.provider.Name(),
		Model:              model,
		AppendSystemPrompt: m.appendSystemPrompt,
		UserMessage:        input.Text,
		MaxSteps:           m.maxSteps,
		Tools:              toolDefsFromSpecs(toolSpecs),
		Session:            m.turnSession(),
	}

	result, err := m.harness.RunTurn(turnCtx, req, newSurfaceRunner(m.surface), sink)

	switch result.StopReason {
	case bridle.StopReasonModelDone, bridle.StopReasonMaxSteps:
		// U-C6: persist the transcript core for a SUCCEEDED turn — the
		// turn's content already streamed to `out` above (sink/emit), so
		// a store failure here must not turn a real success into a
		// turn.failed; see persistTurn's doc comment for the best-effort
		// posture and why aborted/errored turns don't persist at v1.
		if m.store != nil {
			m.persistTurn(input, result)
		}
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

// turnSession (U-C7) computes this turn's bridle.SessionHandle. agora
// CHOOSES the session id — the Manager's threadID, stable for the whole
// thread and opaque to the sidecar — rather than capturing one FROM a
// prior turn's result: claudesdk.go's provider only ECHOES the sidecar's
// session id back for a same-turn invariant check (its "Session
// resume-id invariant" comment), it never mints the id agora hands it.
//
// Whether the real Claude-Code SDK accepts an agora thread id verbatim as
// its own session id is a U-F1-confirm item (the first live-turn smoke
// test, operator watching, per agora-engine-blueprint.md Phase 5) — if
// the sidecar rejects the format, a UUID DERIVED from threadID (e.g. a
// deterministic v5 UUID) is the documented fallback; today's fake
// provider accepts any opaque string, so plain threadID is fine for every
// test in this package.
//
// New is a per-Manager latch, computed ONCE: the FIRST call (sessionStarted
// still false) decides by probing m.store (when non-nil) for prior items
// on this thread via storeHasItems — a non-empty store means an earlier
// process already ran turns on this thread and this Manager is picking
// the conversation back up (New=false, resume); no store, an empty store,
// or a probe error all mean New=true (fresh). That probe never runs
// again — sessionStarted flips true right after this first decision, and
// EVERY LATER turn on this Manager gets New=false unconditionally: once a
// session has been started (fresh or resumed) by this Manager, every turn
// after the first is itself a continuation of what THIS Manager already
// told the provider, regardless of what the first turn's New value was.
func (m *Manager) turnSession() bridle.SessionHandle {
	newSession := true
	if m.sessionStarted {
		newSession = false
	} else {
		if m.store != nil && storeHasItems(m.store, m.threadID) {
			newSession = false
		}
		m.sessionStarted = true
	}
	return bridle.SessionHandle{ID: sessionIDFor(m.threadID), New: newSession}
}

// sessionIDFor maps an agora thread id to the claude-sdk session id. The
// Claude Code SDK REQUIRES the session id to be a valid UUID ("Error: Invalid
// session ID. Must be a valid UUID." — the first live turn hit exactly this
// against a plain thread id like "default"). We derive a deterministic RFC-4122
// v5 UUID from the thread id under a fixed namespace, so the mapping is stable:
// the SAME thread always yields the SAME session UUID across runs, which is what
// makes claude-sdk resume (SessionHandle.New=false on continuation) actually
// reattach to the right server-side session. A pre-existing valid-UUID thread id
// is still re-hashed (harmless — the session id is an opaque handle to the SDK,
// not required to equal the thread id; stability is the only contract).
func sessionIDFor(threadID string) string {
	return uuid.NewSHA1(agoraSessionNamespace, []byte(threadID)).String()
}

// agoraSessionNamespace is the fixed UUIDv5 namespace for deriving claude-sdk
// session ids from thread ids (see sessionIDFor). A stable, arbitrary constant —
// derived once from a URL-namespaced agora label so it never collides with the
// well-known predefined namespaces.
var agoraSessionNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("agora/claude-sdk/session"))

// storeHasItems (U-C7's one-time first-turn resume probe) reports whether
// store already has at least one ThreadItem recorded for threadID — a
// Resume that yields anything at all means an earlier process ran turns
// on this thread already. A probe error (thread not yet Created, a
// transient store fault, ...) is treated the same as "no prior items":
// this is a best-effort continuity signal, not a correctness-critical
// read — worst case a resumable thread starts a fresh provider session
// instead of resuming one, which is the SAFE direction to fail in (a
// fresh session still works; a wrongly-resumed one against a sidecar that
// has no matching state would not).
func storeHasItems(store contracts.ThreadStore, threadID string) bool {
	it, err := store.Resume(threadID)
	if err != nil {
		return false
	}
	defer it.Close()
	_, ok := it.Next()
	return ok
}

// userMessageItemPayload/agentMessageItemPayload are the persisted
// ThreadItem payload shapes for TIUserMessage/TIAgentMessage — a bare
// {"text": ...} object, matching internal/persistence's extractText
// convention (sqlite.go: a payload object with a "text" field is the
// documented FTS-indexable shape) rather than inventing a new one.
type userMessageItemPayload struct {
	Text string `json:"text"`
}

type agentMessageItemPayload struct {
	Text string `json:"text"`
}

// toolCallItemPayload/toolResultItemPayload are the persisted ThreadItem
// payload shapes for TIToolCall/TIToolResult — {id,name,args} and
// {id,result,err} respectively, per the brief. ID correlates the pair
// (same shape purpose as bridle.ToolInvocation.ID); Args/Result ride
// through as raw JSON, unmodified, same posture as this package's
// mcp_tool_call item payloads in sink.go.
type toolCallItemPayload struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type toolResultItemPayload struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Err    string          `json:"err,omitempty"`
}

// persistTurn (U-C6) builds this turn's ThreadItems from input + result
// and Appends them to m.store. Called only from runOneTurn's SUCCESS case
// (StopReasonModelDone/StopReasonMaxSteps), only when m.store != nil —
// see runOneTurn's call site comment for why aborted/errored turns don't
// persist at v1 (the happy path is what this unit's brief asks to get
// solid + tested first; a later unit can decide whether a partial/failed
// turn's tool calls are worth recording too).
//
// Item order matches the brief exactly: TIUserMessage (the input that
// opened this turn) first, then per result.ToolCalls entry, IN ORDER, a
// TIToolCall/TIToolResult pair, then a trailing TIAgentMessage carrying
// FinalText — omitted entirely when FinalText is empty (a tool-only turn
// with no closing model text has nothing to record there; an empty
// TIAgentMessage would be a lie about what the model said).
//
// APPROVAL-decision items (TIApprovalRequest/TIApprovalDecision) are
// DEFERRED — those decisions happen inside the BeforeToolCall hook
// (approval.go's beforeToolCall/Ask rendezvous), not in TurnResult, so
// this turn-boundary call site has no visibility into them. later: a
// per-turn buffer the hook appends into (mirroring how hookTurn already
// threads per-turn state to the hook) is the natural home for that, once
// a unit picks it up — v1 persists the transcript core only: user/tool/
// agent items.
//
// Append is called with a single best-effort posture: on error, this
// function logs to stderr and returns — it does NOT fail the turn. The
// turn already succeeded and its content already streamed to the caller
// over `out` (see runOneTurn's call site comment); a store outage
// shouldn't retroactively turn a real, already-delivered success into a
// turn.failed the caller never asked for. Matches this package's
// existing fail-safe-not-fail-closed posture toward side-channel
// bookkeeping (c.f. Run's doc comment on cancelling an already-finished
// context being "documented-safe" rather than an error condition).
func (m *Manager) persistTurn(input contracts.Input, result bridle.TurnResult) {
	now := time.Now().UTC()
	items := make([]contracts.ThreadItem, 0, 2+2*len(result.ToolCalls))
	items = append(items, contracts.ThreadItem{
		TS:      now,
		Type:    contracts.TIUserMessage,
		Payload: userMessageItemPayload{Text: input.Text},
	})
	for _, tc := range result.ToolCalls {
		items = append(items,
			contracts.ThreadItem{
				TS:      now,
				Type:    contracts.TIToolCall,
				Payload: toolCallItemPayload{ID: tc.ID, Name: tc.Name, Args: tc.Args},
			},
			contracts.ThreadItem{
				TS:      now,
				Type:    contracts.TIToolResult,
				Payload: toolResultItemPayload{ID: tc.ID, Result: tc.Result, Err: tc.Err},
			},
		)
	}
	if result.FinalText != "" {
		items = append(items, contracts.ThreadItem{
			TS:      now,
			Type:    contracts.TIAgentMessage,
			Payload: agentMessageItemPayload{Text: result.FinalText},
		})
	}
	if err := m.store.Append(m.threadID, items); err != nil {
		fmt.Fprintf(os.Stderr, "turnengine: persist turn items for thread %s: %v\n", m.threadID, err)
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
