package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Clock is injected time — ground rule 4: no wall-clock in tests.
type Clock interface{ Now() time.Time }

// SystemClock is the real-time Clock used outside tests.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// NodeStatus is a subagent's run status, tracked by the Manager alongside
// (but distinct from) the graph edge's open/closed status: an edge records
// graph SHAPE (does this subtree still exist), NodeStatus records the
// agent's RUN state.
type NodeStatus string

const (
	NodeRunning     NodeStatus = "running"
	NodeCompleted   NodeStatus = "completed"
	NodeInterrupted NodeStatus = "interrupted"
	NodeErrored     NodeStatus = "errored"
)

// isFinished reports whether a node is eligible for continuation (spec §2:
// continuation "re-opens a *finished* agent" — running agents cannot be
// steered in v1).
func (s NodeStatus) isFinished() bool {
	return s == NodeCompleted || s == NodeInterrupted || s == NodeErrored
}

// SpawnOpts configures one agent() call. Spec: agora-spec-subagents.md §2.
type SpawnOpts struct {
	// AgentType defaults to BuiltinGeneralPurpose when empty.
	AgentType string
	// Model/Effort override the inherited/def value when non-empty.
	Model  string
	Effort contracts.Effort
	// Foreground is the inverse of the wire opt run_in_background: false
	// (spec default) leaves Foreground's zero value = background, matching
	// the spec's stated default without needing a *bool. It marks the graph
	// edge (§2a's cancellation matrix reads it) and is the signal a caller
	// uses to decide whether to await m.Result(id) before continuing its
	// own turn — Spawn itself always returns immediately regardless.
	Foreground bool
	// Isolation: "worktree" or "" (spec §2). Recorded, not acted on — a
	// fresh git worktree is the workspace/fs unit's concern, out of scope
	// here (ground rule 6).
	Isolation string
	// Schema forces structured output when non-nil.
	Schema json.RawMessage
}

// Notification is delivered when a spawned/continued agent finishes —
// spec §2: "when a child finishes, the parent is re-invoked with a
// task-notification containing the result."
type Notification struct {
	AgentID      string
	ParentThread string
	Status       NodeStatus
	Result       RunResult
	Err          error
}

type node struct {
	id         string
	parent     string
	depth      int
	foreground bool
	opts       SpawnOpts
	def        *AgentDef
	effective  EffectiveSpawn

	mu     sync.Mutex
	status NodeStatus
	cancel context.CancelFunc
	done   chan struct{} // closed each time a run/continue attempt finishes
	result RunResult
	runErr error
}

func (n *node) snapshot() (NodeStatus, RunResult, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.status, n.result, n.runErr
}

// Manager orchestrates subagent spawn/cancel/continuation atop a
// contracts.ThreadStore (child threads), a GraphStore (parent/child edges),
// a Registry (agent defs), and a pluggable AgentRunner (turn-engine seam).
// This is the concrete shape of the spec's agent() tool.
type Manager struct {
	store    contracts.ThreadStore
	graph    GraphStore
	registry *Registry
	runner   AgentRunner
	clock    Clock

	// DepthCap: spec §2 "Depth cap (default 1 — subagents can't spawn
	// subagents unless enabled)". A root thread (depth 0, not itself a
	// node the Manager tracks) spawning a child produces depth 1; that
	// child spawning its own child would be depth 2, rejected when
	// DepthCap == 1.
	depthCap int

	// spawnCap: spec §2 per-session spawn cap — a total count of agent()
	// calls this Manager will accept over its lifetime, distinct from
	// depthCap (tree shape) and sem (in-flight concurrency). One Manager
	// instance is taken to be the scope of "session" here (the package has
	// no narrower session concept than the Manager itself).
	spawnCap   int
	spawnCount int // guarded by mu

	sem chan struct{} // concurrency cap (spec §2: "cap concurrent children")

	mu    sync.Mutex
	nodes map[string]*node
	// roots holds ParentContext seeded via RegisterRoot for top-level
	// (non-spawned) parent threads — FIX 5: without an explicit seed, a
	// first-level spawn must fail CLOSED on tools (see Spawn), not fall
	// back to an unrestricted zero-value ParentContext.
	roots  map[string]ParentContext
	notify chan Notification

	// notifyDropped counts notifications dropped because m.notify was full
	// and undrained (FIX 4: the notify send is non-blocking so a full
	// buffer can never leak/block a completion goroutine — see
	// deliverNotification).
	notifyDropped int64
}

// ManagerOption configures NewManager.
type ManagerOption func(*Manager)

// WithClock injects a Clock (tests use a fixed/fake clock — ground rule 4).
func WithClock(c Clock) ManagerOption { return func(m *Manager) { m.clock = c } }

// WithDepthCap overrides the default depth cap of 1.
func WithDepthCap(n int) ManagerOption { return func(m *Manager) { m.depthCap = n } }

// WithMaxConcurrent overrides the default concurrent-spawn cap.
func WithMaxConcurrent(n int) ManagerOption {
	return func(m *Manager) {
		if n < 1 {
			n = 1
		}
		m.sem = make(chan struct{}, n)
	}
}

// defaultSpawnCap is the per-session spawn cap applied when WithSpawnCap is
// not given — generous enough not to bite legitimate heavy workflow use,
// low enough to bound a runaway/adversarial loop of agent() calls.
const defaultSpawnCap = 500

// WithSpawnCap overrides the default per-session spawn cap (spec §2). n<1 is
// treated as 1 (a cap of zero would make the Manager unusable).
func WithSpawnCap(n int) ManagerOption {
	return func(m *Manager) {
		if n < 1 {
			n = 1
		}
		m.spawnCap = n
	}
}

// NewManager builds a Manager. store/graph/runner are required seams;
// registry may be nil (built-ins only).
func NewManager(store contracts.ThreadStore, graph GraphStore, registry *Registry, runner AgentRunner, opts ...ManagerOption) *Manager {
	if registry == nil {
		registry = NewRegistry(nil)
	}
	m := &Manager{
		store:    store,
		graph:    graph,
		registry: registry,
		runner:   runner,
		clock:    SystemClock{},
		depthCap: 1,
		spawnCap: defaultSpawnCap,
		sem:      make(chan struct{}, 16),
		nodes:    make(map[string]*node),
		roots:    make(map[string]ParentContext),
		notify:   make(chan Notification, 256),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// RegisterRoot seeds parentThread's ParentContext (cwd, approval policy,
// permission profile, tool set) for use by first-level agent() spawns whose
// parent is a top-level session/turn thread rather than another spawned
// subagent (a root thread is never itself a *node this package tracks — it
// only appears here as a parent). Spec §2: a child inherits the parent's
// effective tool set; FIX 5's fail-closed rule means a root thread that is
// never registered grants NO tools to a first-level spawn (see Spawn) — the
// turn-engine caller MUST RegisterRoot a thread before its first agent()
// spawn if that thread should be able to grant subagents any tools at all.
func (m *Manager) RegisterRoot(threadID string, pc ParentContext) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roots[threadID] = pc
}

// NotificationsDropped returns the count of notifications dropped because
// m.notify was full and undrained (FIX 4) — an observability hook, not a
// replay mechanism; a production caller should drain Notifications()
// promptly enough that this never grows.
func (m *Manager) NotificationsDropped() int64 {
	return atomic.LoadInt64(&m.notifyDropped)
}

// deliverNotification sends n on m.notify without ever blocking the calling
// completion goroutine (FIX 4: a blocking send here, with nobody draining
// Notifications(), would leak one goroutine per completion forever). When
// the buffer is full, the oldest queued notification is dropped to make
// room — best-effort delivery, not a queue of record — and the drop is
// counted (NotificationsDropped).
func (m *Manager) deliverNotification(n Notification) {
	select {
	case m.notify <- n:
		return
	default:
	}
	select {
	case <-m.notify:
	default:
	}
	select {
	case m.notify <- n:
	default:
		atomic.AddInt64(&m.notifyDropped, 1)
	}
}

// Notifications returns the channel task-completion notifications are
// delivered on (spec §2 "delivered as a turn event/notification"). Buffered
// so background completions never block on a slow/absent consumer within
// this package's own bound (256); a production wiring drains it promptly.
func (m *Manager) Notifications() <-chan Notification { return m.notify }

func newAgentID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("subagent: generate agent id: %w", err)
	}
	return "ag_" + hex.EncodeToString(b[:]), nil
}

// Spawn implements the agent() tool (spec §2). It creates the child thread,
// records the graph edge, and starts the run — inline (blocking) for
// Foreground spawns, in a goroutine for background ones (the spec default).
// Returns the new agent's id.
func (m *Manager) Spawn(ctx context.Context, parentThread, prompt string, opts SpawnOpts) (string, error) {
	if opts.AgentType == "" {
		opts.AgentType = BuiltinGeneralPurpose
	}
	def, _ := m.registry.Get(opts.AgentType)

	m.mu.Lock()
	depth := 1
	var parentCtx ParentContext
	if pn, ok := m.nodes[parentThread]; ok {
		depth = pn.depth + 1
		parentCtx = ParentContext{
			Cwd:    pn.effective.Cwd,
			Policy: pn.effective.Policy,
			Model:  pn.effective.Model,
			Effort: pn.effective.Effort,
			Tools:  pn.effective.Tools,
		}
	} else if rc, ok := m.roots[parentThread]; ok {
		// Explicitly seeded root (RegisterRoot) — honor it verbatim,
		// including a deliberate Tools: nil ("unrestricted") if the caller
		// set that.
		parentCtx = rc
	} else {
		// FIX 5: an UNREGISTERED parent thread must fail CLOSED on tools,
		// not fail open. inherit.go treats parent.Tools == nil as
		// "unrestricted (all tools)" — that is the correct meaning for a
		// deliberately-seeded root with no tool narrowing, but the WRONG
		// default for "we have no idea who this parent is". A non-nil empty
		// slice means "no tools" to ResolveInheritance's intersection logic,
		// so a first-level spawn from an unregistered thread grants nothing
		// (Policy is already a zero-value PolicySet here too, which the
		// approval package treats as fail-safe/ask, per the existing
		// comment this replaces).
		parentCtx = ParentContext{Tools: []string{}}
	}
	if depth > m.depthCap {
		m.mu.Unlock()
		return "", fmt.Errorf("%w: depth %d > cap %d", ErrDepthCapExceeded, depth, m.depthCap)
	}
	if m.spawnCount >= m.spawnCap {
		m.mu.Unlock()
		return "", fmt.Errorf("%w: %d spawns already recorded (cap %d)", ErrSpawnCapExceeded, m.spawnCount, m.spawnCap)
	}
	m.spawnCount++
	m.mu.Unlock()

	eff := ResolveInheritance(parentCtx, def, opts)

	childID, err := newAgentID()
	if err != nil {
		return "", err
	}
	now := m.clock.Now()
	if err := m.store.Create(contracts.ThreadMeta{
		ThreadID:     childID,
		CreatedAt:    now,
		ParentThread: parentThread,
	}); err != nil {
		return "", fmt.Errorf("subagent: create child thread: %w", err)
	}
	if err := m.graph.AddEdge(Edge{
		ParentThread: parentThread,
		ChildThread:  childID,
		Status:       EdgeOpen,
		Foreground:   opts.Foreground,
		CreatedAt:    now,
	}); err != nil {
		return "", fmt.Errorf("subagent: add graph edge: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	n := &node{
		id:         childID,
		parent:     parentThread,
		depth:      depth,
		foreground: opts.Foreground,
		opts:       opts,
		def:        def,
		effective:  eff,
		status:     NodeRunning,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	m.mu.Lock()
	m.nodes[childID] = n
	m.mu.Unlock()

	req := RunRequest{
		AgentID:      childID,
		ParentThread: parentThread,
		AgentType:    opts.AgentType,
		Prompt:       prompt,
		Model:        eff.Model,
		Effort:       eff.Effort,
		Tools:        eff.Tools,
		Schema:       opts.Schema,
	}

	// myDone is captured once, at this attempt's own done channel — closed
	// exactly once by this run, regardless of any later reassignment of
	// n.done by a subsequent Continue. Reading n.done fresh at close time
	// instead would race a Continue that reassigns n.done concurrently
	// (double-close panic if two attempts' closes ever both resolved to the
	// same current field value).
	myDone := n.done
	run := func() {
		res, err := runWithSchemaRetry(runCtx, m.runner, req)
		n.mu.Lock()
		n.result = res
		n.runErr = err
		switch {
		case err != nil:
			if runCtx.Err() != nil {
				n.status = NodeInterrupted
			} else {
				n.status = NodeErrored
			}
		default:
			n.status = NodeCompleted
		}
		status := n.status
		n.mu.Unlock()
		// Persist the terminal outcome (agora#158). Until this existed the
		// run status — which cancel.go calls "the source of truth for is it
		// still running" — lived only in this in-memory map, so nothing
		// could answer "did this child finish, and how?" once the process
		// exited. The graph recorded shape and not outcome, leaving
		// completed, errored and abandoned-mid-run indistinguishable on
		// disk.
		//
		// Best-effort and non-fatal, exactly as cancel.go treats its graph
		// write: the result is already computed and a graph write must never
		// fail it.
		if m.graph != nil && status.isFinished() {
			if oerr := m.graph.RecordOutcome(parentThread, childID, status, m.clock.Now()); oerr != nil {
				fmt.Fprintf(os.Stderr, "subagent: record outcome %s->%s (%s): %v\n", parentThread, childID, status, oerr)
			}
		}
		// FIX 3: notify BEFORE close(myDone). A caller that blocks on
		// Result()/Status() (observes myDone closed) and then
		// non-blocking-checks Notifications() must never be able to miss
		// this notification — sequencing the send first guarantees it is
		// already queued by the time the done-close is observable.
		m.deliverNotification(Notification{AgentID: childID, ParentThread: parentThread, Status: status, Result: res, Err: err})
		close(myDone)
	}

	// Spawn always returns immediately with agent_id (spec §2: "returns
	// immediately with agent_id; completion delivered as a turn
	// event/notification") — every spawn runs concurrently (spec §2:
	// "multiple agent() calls in one assistant turn run concurrently"),
	// queueing on the concurrency cap inside its own goroutine rather than
	// blocking Spawn. opts.Foreground governs the graph edge's Foreground
	// bit (the §2a cancellation matrix) and is the signal a CALLER uses to
	// decide whether to await m.Result(id) before proceeding with its own
	// turn — that blocking-the-turn policy lives at the caller (turn-engine)
	// layer, not inside Spawn itself.
	go func() {
		m.sem <- struct{}{}
		defer func() { <-m.sem }()
		run()
	}()

	return childID, nil
}

// Continue implements send_message(agent_id, message) — spec §2: "re-opens
// a *finished* agent with its context intact". Returns ErrNodeNotFound if
// agentID is unknown, or ErrNotFinished if the agent is currently running
// (whether because it genuinely never finished, or because a concurrent
// Continue won the race to resume it first — see the atomic check-and-set
// below, FIX 2). Otherwise re-runs the agent with message as the new
// prompt and blocks until that attempt completes, mirroring Spawn's
// foreground behavior (a continuation is always awaited by its caller in
// this package — background re-dispatch of a continuation is a
// caller-level concern, e.g. the workflow engine).
func (m *Manager) Continue(ctx context.Context, agentID, message string) (RunResult, error) {
	m.mu.Lock()
	n, ok := m.nodes[agentID]
	m.mu.Unlock()
	if !ok {
		return RunResult{}, fmt.Errorf("%w: %s", ErrNodeNotFound, agentID)
	}

	runCtx, cancel := context.WithCancel(ctx)
	myDone := make(chan struct{})

	// FIX 2: the finished->running transition is a single atomic
	// check-and-set under ONE lock hold — no unlock between the isFinished
	// check and the status/cancel/done writes. Two concurrent Continue
	// calls on the same agent now cannot both observe "finished": exactly
	// one wins this critical section (sees status finished, flips it to
	// Running) and the other loses (sees Running, already flipped by the
	// winner, and fails with ErrNotFinished) — no TOCTOU window where both
	// proceed and both later reassign/close n.done.
	n.mu.Lock()
	if !n.status.isFinished() {
		st := n.status
		n.mu.Unlock()
		cancel()
		return RunResult{}, fmt.Errorf("%w: agent %s is %s", ErrNotFinished, agentID, st)
	}
	prevStatus := n.status
	n.status = NodeRunning
	n.cancel = cancel
	n.done = myDone
	n.mu.Unlock()

	// A cancelled/closed edge is still resumable-by-continuation (spec
	// §2a): reopen it. This only runs for the single winner of the
	// check-and-set above, so it is not itself racy.
	if e, found, _ := m.graph.Edge(n.parent, agentID); found && e.Status == EdgeClosed {
		if err := m.graph.ReopenEdge(n.parent, agentID); err != nil {
			// Revert the claimed transition so the node is not stuck
			// Running with nothing to ever close myDone — a later Continue
			// (or the invariant checked by CancelNode) must see it as
			// still resumable, not orphaned running.
			n.mu.Lock()
			n.status = prevStatus
			n.mu.Unlock()
			cancel()
			close(myDone)
			return RunResult{}, fmt.Errorf("subagent: reopen edge on continue: %w", err)
		}
	}

	req := RunRequest{
		AgentID:      agentID,
		ParentThread: n.parent,
		AgentType:    n.opts.AgentType,
		Prompt:       message,
		Model:        n.effective.Model,
		Effort:       n.effective.Effort,
		Tools:        n.effective.Tools,
		Schema:       n.opts.Schema,
	}

	m.sem <- struct{}{}
	res, err := runWithSchemaRetry(runCtx, m.runner, req)
	<-m.sem

	n.mu.Lock()
	n.result = res
	n.runErr = err
	switch {
	case err != nil:
		if runCtx.Err() != nil {
			n.status = NodeInterrupted
		} else {
			n.status = NodeErrored
		}
	default:
		n.status = NodeCompleted
	}
	status := n.status
	n.mu.Unlock()

	// FIX 3 (see run()'s matching comment): notify before close(myDone).
	// FIX 2: close myDone — the channel THIS attempt captured, never a
	// fresh read of n.done (which a subsequent Continue may have already
	// reassigned) — the same double-close hazard Spawn's run() already
	// guarded against via its myDone capture.
	m.deliverNotification(Notification{AgentID: agentID, ParentThread: n.parent, Status: status, Result: res, Err: err})
	close(myDone)
	return res, err
}

// Status returns the current run status of agentID.
func (m *Manager) Status(agentID string) (NodeStatus, bool) {
	m.mu.Lock()
	n, ok := m.nodes[agentID]
	m.mu.Unlock()
	if !ok {
		return "", false
	}
	st, _, _ := n.snapshot()
	return st, true
}

// Result returns the last completed/errored/interrupted RunResult for
// agentID, blocking until the current run finishes if it is still running.
func (m *Manager) Result(agentID string) (RunResult, error, bool) {
	m.mu.Lock()
	n, ok := m.nodes[agentID]
	m.mu.Unlock()
	if !ok {
		return RunResult{}, nil, false
	}
	n.mu.Lock()
	running := n.status == NodeRunning
	done := n.done
	n.mu.Unlock()
	if running {
		<-done
	}
	_, res, err := n.snapshot()
	return res, err, true
}

// Children returns agentID's direct child agent ids, deterministic order.
func (m *Manager) Children(agentID string) ([]string, error) {
	edges, err := m.graph.Children(agentID, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(edges))
	for i, e := range edges {
		out[i] = e.ChildThread
	}
	sort.Strings(out)
	return out, nil
}
