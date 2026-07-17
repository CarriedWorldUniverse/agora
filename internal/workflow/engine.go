package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
	"go.starlark.net/starlark"
)

// Status is the terminal (or suspended) state of one Run attempt.
type Status string

const (
	StatusCompleted Status = "completed"
	StatusErrored   Status = "errored"
	// StatusParked: the run raised a ctx.question/ctx.approval (directly, or
	// via a bubbled agent question) with no cached answer available —
	// spec §2: "the RUN parks (waiting-on-answer run status ...); no thread
	// is held". Run returns immediately; nothing is blocked.
	StatusParked Status = "parked"
)

// ParkedInfo describes what a StatusParked outcome is waiting on.
type ParkedInfo struct {
	Kind       EntryKind
	QuestionID string
	Args       contracts.QuestionArgs
}

// Outcome is what one Run attempt produced.
type Outcome struct {
	Status Status
	// Result is main()'s return value, canonical-JSON-encoded
	// (StatusCompleted only).
	Result json.RawMessage
	// Err is the script/engine error (StatusErrored only).
	Err error
	// Parked is set for StatusParked.
	Parked *ParkedInfo
	// Entries is the complete journal this attempt produced (replayed
	// entries carried forward plus any newly-live ones), in Seq order —
	// exactly what was handed to Journal.Save. Exposed directly so callers
	// (tests, a future CLI) don't need a round trip through JournalStore.
	Entries []Entry
}

// RunOptions configures one Run attempt (fresh or resumed — the two are
// the same call: Run always re-executes the script from the top and lets
// the journal decide what replays, spec §4).
type RunOptions struct {
	// RunID identifies the run for journal lookup/persistence. Required.
	RunID string
	// ThreadID is the run's own thread id in Questions.Store — required
	// whenever the script can reach a ctx.question/ctx.approval call (or an
	// agent bubble). The caller must have already created this thread
	// (contracts.ThreadStore.Create) before calling Run; QuestionRouter
	// does not create it (mirrors internal/planning's own tests, which
	// create the thread before building a QuestionLog over it).
	ThreadID string
	Script   []byte
	Filename string
	// Args is the JSON-shaped value bound to ctx.args / main's args
	// parameter — a map[string]any (or nil for no args).
	Args any

	Clock     Clock
	Invoker   AgentInvoker
	Questions *QuestionRouter
	Journal   JournalStore
	// Identity attributes ctx.question/ctx.approval asks in the audit trail
	// (planning.AskRequest.Identity).
	Identity string

	MaxConcurrent    int
	LifetimeAgentCap int
	PerCallItemCap   int
	// LifetimeBranchCap bounds the TOTAL number of ctx.parallel/ctx.pipeline
	// goroutines a run may spawn across its whole lifetime, at any nesting
	// depth — review finding: "nested parallel/pipeline goroutine
	// explosion." PerCallItemCap alone only bounds a single call's width; a
	// script that recursively fans out (each leaf calling ctx.parallel
	// again) can still spawn goroutines exponentially in depth before any
	// one call's item count or the agent lifetime cap is ever hit. Default
	// defaultLifetimeBranchCap if unset/non-positive.
	LifetimeBranchCap int
	// MaxSteps bounds the number of starlark abstract-computation steps any
	// ONE starlark.Thread this run creates may execute before it is
	// force-canceled — review finding: "no starlark step budget /
	// uncancellable infinite loop." Applies to loadThread, mainThread, and
	// every ctx.parallel/ctx.pipeline child thread. Default defaultMaxSteps
	// if unset/zero.
	MaxSteps uint64
}

const (
	defaultLifetimeAgentCap  = 1000
	defaultPerCallItemCap    = 4096
	defaultLifetimeBranchCap = 10_000
	// defaultMaxSteps: a sane backstop against a runaway loop
	// (`while True: pass`) that doesn't break any legitimate script — 1e9
	// abstract computation steps is orders of magnitude more than any
	// workflow script's control flow should need.
	defaultMaxSteps uint64 = 1_000_000_000
)

// newGuardedThread builds a starlark.Thread with this run's step budget
// (finding 1) and wires goCtx's cancellation into it: a watcher goroutine
// calls thread.Cancel as soon as goCtx is Done, which starlark's
// interpreter observes on its next step (thread.Cancel/SetMaxExecutionSteps
// are both documented goroutine-safe). The caller MUST invoke the returned
// stop func once the thread is done running (normally via defer) to release
// the watcher goroutine — every starlark.Thread this package creates goes
// through here, so a run never has an uncancellable, unbounded thread.
func newGuardedThread(goCtx context.Context, name string, maxSteps uint64) (*starlark.Thread, func()) {
	if maxSteps == 0 {
		maxSteps = defaultMaxSteps
	}
	th := &starlark.Thread{Name: name}
	th.SetMaxExecutionSteps(maxSteps)
	done := make(chan struct{})
	go func() {
		select {
		case <-goCtx.Done():
			th.Cancel("workflow: context canceled: " + goCtx.Err().Error())
		case <-done:
		}
	}()
	// stop is idempotent (sync.Once) so callers can BOTH stop promptly after
	// the thread finishes AND `defer stop()` as a panic-safety net without
	// risking a double close(done) panic — a bare call that skips on a
	// starlark panic would otherwise leak this watcher goroutine.
	var once sync.Once
	return th, func() { once.Do(func() { close(done) }) }
}

// defaultMaxConcurrent implements spec §3: "min(16, cores-2)".
func defaultMaxConcurrent() int {
	n := runtime.NumCPU() - 2
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	return n
}

// branchLocalKey is the starlark.Thread.Local key each goroutine's Thread
// carries its *branchState under — see journal.go's Entry doc comment for
// why replay keys are (Branch, LocalSeq) rather than a flat run-wide
// counter.
const branchLocalKey = "workflow_branch"

// branchState is owned by exactly one goroutine (the one running the
// starlark.Thread it is attached to) — starlark itself never runs a single
// Thread's calls concurrently, so seq needs no synchronization.
type branchState struct {
	path string
	seq  int64
}

func currentBranch(thread *starlark.Thread) *branchState {
	if b, ok := thread.Local(branchLocalKey).(*branchState); ok && b != nil {
		return b
	}
	// Unreachable in normal operation: Run/parallel/pipeline always seed
	// this before calling into script code. Review finding: a silent
	// &branchState{path:""} fallback here would let multiple concurrent
	// callers collide at the SAME (Branch="", LocalSeq=0) journal key,
	// silently corrupting the journal — a loud panic is strictly safer
	// than that silent corruption, since this can only mean an internal
	// invariant was violated (a builtin invoked from a starlark.Thread the
	// engine itself never set up).
	panic("workflow: internal invariant violated: currentBranch called on a starlark.Thread with no branchState — every Thread this package creates must SetLocal(branchLocalKey, ...) before running script code")
}

// errParked is the sentinel a ctx.question/ctx.approval call (directly, or
// via ctx.agent's bubble handling) returns to suspend the run. It
// propagates as an ordinary Go error through starlark's call stack (ending
// main()'s Call), which Run recognizes via errors.As.
type errParked struct {
	Kind       EntryKind
	QuestionID string
	Args       contracts.QuestionArgs
}

func (e *errParked) Error() string {
	return fmt.Sprintf("workflow: run parked on %s %s", e.Kind, e.QuestionID)
}

// runState is the shared, concurrency-safe state one Run attempt's
// goroutines (root + every ctx.parallel/pipeline branch) all read/write
// through. mu guards everything below it; oldIndex is read-only after
// construction (built before any goroutine starts) so needs no lock.
type runState struct {
	goCtx     context.Context
	clock     Clock
	invoker   AgentInvoker
	questions *QuestionRouter
	journal   JournalStore
	runID     string
	threadID  string
	identity  string
	meta      *Meta

	perCallItemCap    int
	lifetimeCap       int
	lifetimeBranchCap int
	maxSteps          uint64
	sem               chan struct{}

	oldIndex map[key]Entry

	mu             sync.Mutex
	out            []Entry
	nextGlobalSeq  int64
	agentCount     int64
	branchSpawnCnt int64

	// saveMu serializes record()'s ENTIRE snapshot+persist sequence — held
	// across both the rs.mu-guarded append/snapshot AND the journal.Save
	// call itself. Finding 11 ("concurrent FileJournalStore.Save races"):
	// without this, two goroutines' record() calls could snapshot rs.out
	// under rs.mu, release it, and then call journal.Save in the OPPOSITE
	// order from the order they snapshotted — an older/shorter snapshot
	// landing on disk AFTER a newer/longer one, silently losing whatever
	// entry only the newer snapshot had. Holding saveMu across the whole
	// sequence makes "snapshot" and "Save" one atomic unit per run, so
	// Save calls for this run are strictly ordered by recency and can
	// never race each other.
	saveMu sync.Mutex
}

// newGuardedThread is rs's convenience wrapper around the package-level
// newGuardedThread — every ctx.parallel/ctx.pipeline child thread goes
// through here so it inherits this run's step budget and cancellation
// (finding 1).
func (rs *runState) newGuardedThread(name string) (*starlark.Thread, func()) {
	return newGuardedThread(rs.goCtx, name, rs.maxSteps)
}

// beginBranchSpawn enforces the run-lifetime branch-goroutine cap (finding
// 3): called BEFORE every `go func` in parallel/pipeline, never as a
// blocking semaphore (a blocking semaphore a parent holds while awaiting
// its children would deadlock on nested fan-out) — just a monotonic,
// mutex-guarded counter that fails closed once the run-wide total is
// exceeded.
func (rs *runState) beginBranchSpawn() error {
	rs.mu.Lock()
	rs.branchSpawnCnt++
	n := rs.branchSpawnCnt
	rs.mu.Unlock()
	if n > int64(rs.lifetimeBranchCap) {
		return fmt.Errorf("%w: %d > %d", ErrLifetimeBranchCapExceeded, n, rs.lifetimeBranchCap)
	}
	return nil
}

// tryReplay looks up (branch, localSeq, kind) in the prior journal;
// returns ok=false on a cache miss OR a hash mismatch (spec §4: "the first
// mismatch and everything after runs live" — enforced here per-call by the
// hash comparison, not by any separate divergence flag: once a call's
// fresh hash stops matching what was recorded at its position, this
// returns false and the caller runs it live, same as if the position had
// never been recorded at all).
func (rs *runState) tryReplay(branch string, localSeq int64, kind EntryKind, hash string) (Entry, bool) {
	e, ok := rs.oldIndex[key{Branch: branch, LocalSeq: localSeq, Kind: kind}]
	if !ok || e.Hash != hash {
		return Entry{}, false
	}
	return e, true
}

// record appends e (Seq assigned here) to this attempt's output and
// persists the whole journal so far — see journal.go's FileJournalStore
// doc comment for the incremental-Save tradeoff this accepts.
func (rs *runState) record(e Entry) error {
	rs.saveMu.Lock()
	defer rs.saveMu.Unlock()

	rs.mu.Lock()
	e.Seq = rs.nextGlobalSeq
	rs.nextGlobalSeq++
	rs.out = append(rs.out, e)
	snapshot := make([]Entry, len(rs.out))
	copy(snapshot, rs.out)
	rs.mu.Unlock()

	sortBySeq(snapshot)
	if err := rs.journal.Save(rs.runID, snapshot); err != nil {
		return fmt.Errorf("workflow: persist journal: %w", err)
	}
	return nil
}

// beginAgentCall enforces the lifetime agent cap (spec §3) and the
// concurrency cap (acquiring rs.sem) before a real agent invocation.
func (rs *runState) beginAgentCall() error {
	rs.mu.Lock()
	rs.agentCount++
	n := rs.agentCount
	rs.mu.Unlock()
	if n > int64(rs.lifetimeCap) {
		return fmt.Errorf("%w: %d > %d", ErrLifetimeCapExceeded, n, rs.lifetimeCap)
	}
	select {
	case rs.sem <- struct{}{}:
		return nil
	case <-rs.goCtx.Done():
		return rs.goCtx.Err()
	}
}

func (rs *runState) endAgentCall() { <-rs.sem }

// askOrApprove is the single implementation behind ctx.question,
// ctx.approval, and ctx.agent's bubbled-question handling — spec §2:
// "Same pipeline as ctx.question and ctx.approval — one implementation,
// two verbs." kind distinguishes the journal entry shape only; the
// park/replay/re-ask mechanics are identical.
func (rs *runState) askOrApprove(kind EntryKind, branch string, localSeq int64, args contracts.QuestionArgs, source contracts.QuestionSource) (contracts.Answer, error) {
	goVal, err := jsonRawToGo(mustMarshal(args))
	if err != nil {
		return contracts.Answer{}, err
	}
	hash, err := contentHash(goVal)
	if err != nil {
		return contracts.Answer{}, fmt.Errorf("workflow: hash question payload: %w", err)
	}

	if cached, ok := rs.tryReplay(branch, localSeq, kind, hash); ok {
		if cached.QuestionID == "" {
			// Finding 8: this position previously tried to raise a
			// question but lost the race for the thread's single
			// blocking-question slot (planning.ErrAlreadyParked — a
			// sibling ctx.parallel/pipeline branch got there first) — no
			// question id was ever minted, so there is nothing to look up.
			// Retry live now: the sibling that DID park may have been
			// answered (and un-parked the thread) since.
			return rs.raiseNow(kind, branch, localSeq, hash, args, source)
		}

		// Finding 4: an answered/still-parked cached entry is ALWAYS
		// re-derived from the authoritative planning store, never trusted
		// from the journal's own Answer bytes — cached.Answer is an audit
		// copy only. (The replay key (branch,localSeq,kind,hash) is
		// offline-computable and FileJournalStore writes 0644 with no
		// integrity binding, so a tampered journal.jsonl could otherwise
		// forge cached.Answer bytes and bypass the human-approval gate.)
		ans, answered, err := rs.questions.lookupAnswer(rs.threadID, cached.QuestionID)
		if err != nil {
			return contracts.Answer{}, err
		}
		if answered {
			answeredEntry := cached
			ab, err := json.Marshal(ans)
			if err != nil {
				return contracts.Answer{}, err
			}
			answeredEntry.Answer = ab
			answeredEntry.By = ans.By
			if err := rs.record(answeredEntry); err != nil {
				return contracts.Answer{}, err
			}
			return ans, nil
		}

		// Still unanswered: recovery via replay-and-re-ask (spec, this
		// unit's DoD) — carry the same still-open card forward into this
		// attempt's journal and re-surface it, without asking planning
		// again (the thread is already durably parked from before;
		// re-calling Ask would just error ErrAlreadyParked).
		if err := rs.record(cached); err != nil {
			return contracts.Answer{}, err
		}
		return contracts.Answer{}, &errParked{Kind: kind, QuestionID: cached.QuestionID, Args: args}
	}

	// Never asked before at this position: raise it now.
	return rs.raiseNow(kind, branch, localSeq, hash, args, source)
}

// raiseNow raises args as a brand-new question against the planning store —
// either genuinely never asked before at this position, or a finding-8
// retry of a position that previously lost the single-parked-slot race.
// Always parks (askContext resolves to DispositionPark unconditionally),
// UNLESS the thread is already parked on a DIFFERENT question
// (planning.ErrAlreadyParked: this thread allows only one blocking question
// at a time), in which case a "pending" marker (QuestionID=="") is
// journaled instead of letting the question vanish with no trace (finding
// 8: previously this returned a plain error that ctx.parallel's error scan
// didn't recognize as a park, so the losing sibling's question was silently
// dropped — the thunk failed to None with zero journal trace).
func (rs *runState) raiseNow(kind EntryKind, branch string, localSeq int64, hash string, args contracts.QuestionArgs, source contracts.QuestionSource) (contracts.Answer, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return contracts.Answer{}, err
	}

	outcome, err := rs.questions.Ask(rs.threadID, rs.identity, args, source, rs.clock.Now())
	if err != nil {
		if errors.Is(err, planning.ErrAlreadyParked) {
			if err := rs.record(Entry{
				Branch: branch, LocalSeq: localSeq, Kind: kind, Hash: hash, Args: argsJSON,
			}); err != nil {
				return contracts.Answer{}, err
			}
			return contracts.Answer{}, &errParked{Kind: kind, QuestionID: "", Args: args}
		}
		return contracts.Answer{}, fmt.Errorf("workflow: ask: %w", err)
	}
	if err := rs.record(Entry{
		Branch: branch, LocalSeq: localSeq, Kind: kind, Hash: hash,
		QuestionID: outcome.Question.ID, Args: argsJSON,
	}); err != nil {
		return contracts.Answer{}, err
	}
	return contracts.Answer{}, &errParked{Kind: kind, QuestionID: outcome.Question.ID, Args: args}
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// v is always a contracts.QuestionArgs literal built by this
		// package — a marshal failure here would mean a non-JSON-safe
		// field slipped into that type, a programmer error, not a runtime
		// condition callers can act on.
		panic(fmt.Sprintf("workflow: marshal QuestionArgs: %v", err))
	}
	return b
}

// Run executes opts.Script once: a fresh run if opts.RunID has never been
// saved to opts.Journal, or a resume otherwise (replaying whatever prefix
// of calls still hashes the same — spec §4). There is no separate "resume"
// entry point; determinism-by-construction is what makes one function
// correct for both cases.
func Run(ctx context.Context, opts RunOptions) (Outcome, error) {
	if opts.RunID == "" {
		return Outcome{}, fmt.Errorf("workflow: RunOptions.RunID is required")
	}
	if opts.Clock == nil {
		opts.Clock = SystemClock{}
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = defaultMaxConcurrent()
	}
	if opts.LifetimeAgentCap <= 0 {
		opts.LifetimeAgentCap = defaultLifetimeAgentCap
	}
	if opts.PerCallItemCap <= 0 {
		opts.PerCallItemCap = defaultPerCallItemCap
	}
	if opts.LifetimeBranchCap <= 0 {
		opts.LifetimeBranchCap = defaultLifetimeBranchCap
	}
	if opts.MaxSteps == 0 {
		opts.MaxSteps = defaultMaxSteps
	}
	if opts.Journal == nil {
		opts.Journal = NewMemJournalStore()
	}
	if opts.Filename == "" {
		opts.Filename = opts.RunID + ".star"
	}

	old, err := opts.Journal.Read(opts.RunID)
	if err != nil {
		return Outcome{}, fmt.Errorf("workflow: read prior journal: %w", err)
	}

	// Finding 1: every starlark.Thread this run creates gets a step budget
	// and observes ctx cancellation — loadThread included, since script
	// TOP-LEVEL code (not just main()) can in principle loop too.
	loadThread, stopLoad := newGuardedThread(ctx, "wf-load:"+opts.RunID, opts.MaxSteps)
	defer stopLoad() // panic-safety net; the bare stopLoad() below stops it promptly on the normal path
	predeclared := starlark.StringDict{
		"workflow_meta": starlark.NewBuiltin("workflow_meta", workflowMetaBuiltin),
	}
	globals, err := starlark.ExecFile(loadThread, opts.Filename, opts.Script, predeclared)
	stopLoad()
	if err != nil {
		return Outcome{}, fmt.Errorf("workflow: exec script: %w", err)
	}

	meta, _ := loadThread.Local(metaKey).(*Meta)
	if meta == nil {
		return Outcome{}, ErrNoMeta
	}
	mainFn, ok := globals["main"]
	if !ok {
		return Outcome{}, ErrNoMain
	}

	// Freeze the whole top-level environment: ctx.parallel/pipeline thunks
	// are lambdas that close over it, and those run on their own goroutines
	// with their own starlark.Thread — go.starlark.net only guarantees
	// concurrent use of values that have been frozen (Value.Freeze).
	globals.Freeze()

	argsVal, err := toStarlark(opts.Args)
	if err != nil {
		return Outcome{}, fmt.Errorf("workflow: convert args: %w", err)
	}
	argsVal.Freeze()

	rs := &runState{
		goCtx:             ctx,
		clock:             opts.Clock,
		invoker:           opts.Invoker,
		questions:         opts.Questions,
		journal:           opts.Journal,
		runID:             opts.RunID,
		threadID:          opts.ThreadID,
		identity:          opts.Identity,
		meta:              meta,
		perCallItemCap:    opts.PerCallItemCap,
		lifetimeCap:       opts.LifetimeAgentCap,
		lifetimeBranchCap: opts.LifetimeBranchCap,
		maxSteps:          opts.MaxSteps,
		sem:               make(chan struct{}, opts.MaxConcurrent),
		oldIndex:          indexEntries(old),
	}

	// Finding 9: ctx.now must be the SAME frozen instant across every
	// resume of this run (spec §2: "ctx.now ... the only clock" —
	// otherwise a resume that reconstructs a different `now` busts every
	// ctx.now-dependent hash). Reuse the instant a prior attempt already
	// persisted (EntryRunStart), if any; a fresh run mints one now from
	// opts.Clock and persists it as the very first thing this attempt
	// records, so every later Save (including the final one below) carries
	// it forward regardless of what Clock a FUTURE resume is given.
	var frozenNowStr string
	for _, e := range old {
		if e.Kind == EntryRunStart {
			frozenNowStr = e.Message
			break
		}
	}
	if frozenNowStr == "" {
		frozenNowStr = opts.Clock.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := rs.record(Entry{Branch: "", LocalSeq: 0, Kind: EntryRunStart, Message: frozenNowStr}); err != nil {
		return Outcome{}, fmt.Errorf("workflow: persist run start: %w", err)
	}

	nowVal := starlark.String(frozenNowStr)
	cObj := &ctxObj{rs: rs, args: argsVal, now: nowVal, budget: &budgetObj{}}

	mainThread, stopMain := rs.newGuardedThread("wf-main:" + opts.RunID)
	defer stopMain() // panic-safety net; the bare stopMain() below stops it promptly on the normal path
	mainThread.SetLocal(branchLocalKey, &branchState{path: ""})

	result, callErr := starlark.Call(mainThread, mainFn, starlark.Tuple{cObj, argsVal}, nil)
	stopMain()

	rs.mu.Lock()
	entries := make([]Entry, len(rs.out))
	copy(entries, rs.out)
	rs.mu.Unlock()
	sortBySeq(entries)
	// Final save covers the (valid, if unusual) zero-call-run case where
	// record() was never called at all.
	if err := opts.Journal.Save(opts.RunID, entries); err != nil {
		return Outcome{}, fmt.Errorf("workflow: persist final journal: %w", err)
	}

	if callErr != nil {
		var pe *errParked
		if errors.As(callErr, &pe) {
			return Outcome{
				Status:  StatusParked,
				Entries: entries,
				Parked:  &ParkedInfo{Kind: pe.Kind, QuestionID: pe.QuestionID, Args: pe.Args},
			}, nil
		}
		return Outcome{Status: StatusErrored, Entries: entries, Err: callErr}, nil
	}

	resGo, err := toGo(result)
	if err != nil {
		return Outcome{Status: StatusErrored, Entries: entries, Err: err}, nil
	}
	resJSON, err := canonicalJSON(resGo)
	if err != nil {
		return Outcome{Status: StatusErrored, Entries: entries, Err: err}, nil
	}
	return Outcome{Status: StatusCompleted, Entries: entries, Result: resJSON}, nil
}
