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
}

const (
	defaultLifetimeAgentCap = 1000
	defaultPerCallItemCap   = 4096
)

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
	// Defensive fallback: Run/parallel/pipeline always seed this before
	// calling into script code, so this only fires if a builtin is somehow
	// invoked from a Thread the engine didn't set up.
	return &branchState{path: ""}
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

	perCallItemCap int
	lifetimeCap    int
	sem            chan struct{}

	oldIndex map[key]Entry

	mu            sync.Mutex
	out           []Entry
	nextGlobalSeq int64
	agentCount    int64
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
		if len(cached.Answer) > 0 {
			var ans contracts.Answer
			if err := json.Unmarshal(cached.Answer, &ans); err != nil {
				return contracts.Answer{}, fmt.Errorf("workflow: decode cached answer: %w", err)
			}
			if err := rs.record(cached); err != nil {
				return contracts.Answer{}, err
			}
			return ans, nil
		}

		// A still-parked marker from a previous attempt at this exact
		// position: check whether it has since been answered (spec §2's
		// "a daemon restart mid-question replays to the unanswered call
		// and re-raises it" fallback — this is the branch that lets a
		// resume pick up a real answer instead of just re-raising).
		ans, answered, err := rs.questions.lookupAnswer(rs.threadID, cached.QuestionID)
		if err != nil {
			return contracts.Answer{}, err
		}
		if answered {
			answered := cached
			ab, err := json.Marshal(ans)
			if err != nil {
				return contracts.Answer{}, err
			}
			answered.Answer = ab
			answered.By = ans.By
			if err := rs.record(answered); err != nil {
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

	// Never asked before at this position: raise it now (always parks —
	// askContext resolves to DispositionPark unconditionally).
	outcome, err := rs.questions.Ask(rs.threadID, rs.identity, args, source, rs.clock.Now())
	if err != nil {
		return contracts.Answer{}, fmt.Errorf("workflow: ask: %w", err)
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return contracts.Answer{}, err
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

	loadThread := &starlark.Thread{Name: "wf-load:" + opts.RunID}
	predeclared := starlark.StringDict{
		"workflow_meta": starlark.NewBuiltin("workflow_meta", workflowMetaBuiltin),
	}
	globals, err := starlark.ExecFile(loadThread, opts.Filename, opts.Script, predeclared)
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
		goCtx:          ctx,
		clock:          opts.Clock,
		invoker:        opts.Invoker,
		questions:      opts.Questions,
		journal:        opts.Journal,
		runID:          opts.RunID,
		threadID:       opts.ThreadID,
		identity:       opts.Identity,
		meta:           meta,
		perCallItemCap: opts.PerCallItemCap,
		lifetimeCap:    opts.LifetimeAgentCap,
		sem:            make(chan struct{}, opts.MaxConcurrent),
		oldIndex:       indexEntries(old),
	}

	nowVal := starlark.String(opts.Clock.Now().UTC().Format(time.RFC3339Nano))
	cObj := &ctxObj{rs: rs, args: argsVal, now: nowVal, budget: &budgetObj{}}

	mainThread := &starlark.Thread{Name: "wf-main:" + opts.RunID}
	mainThread.SetLocal(branchLocalKey, &branchState{path: ""})

	result, callErr := starlark.Call(mainThread, mainFn, starlark.Tuple{cObj, argsVal}, nil)

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
