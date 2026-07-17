package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
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

	sem chan struct{} // concurrency cap (spec §2: "cap concurrent children")

	mu     sync.Mutex
	nodes  map[string]*node
	notify chan Notification
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
		sem:      make(chan struct{}, 16),
		nodes:    make(map[string]*node),
		notify:   make(chan Notification, 256),
	}
	for _, o := range opts {
		o(m)
	}
	return m
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
	var parentPolicy contracts.PolicySet
	var parentModel string
	var parentEffort contracts.Effort
	var parentTools []string
	if pn, ok := m.nodes[parentThread]; ok {
		depth = pn.depth + 1
		parentPolicy = pn.effective.Policy
		parentModel = pn.effective.Model
		parentEffort = pn.effective.Effort
		parentTools = pn.effective.Tools
	}
	if depth > m.depthCap {
		m.mu.Unlock()
		return "", fmt.Errorf("%w: depth %d > cap %d", ErrDepthCapExceeded, depth, m.depthCap)
	}
	m.mu.Unlock()

	eff := ResolveInheritance(ParentContext{
		Policy: parentPolicy,
		Model:  parentModel,
		Effort: parentEffort,
		Tools:  parentTools,
	}, def, opts)

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
		close(myDone)
		m.notify <- Notification{AgentID: childID, ParentThread: parentThread, Status: status, Result: res, Err: err}
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
// a *finished* agent with its context intact". Returns ErrNodeNotFound or
// ErrNotFinished as appropriate; otherwise re-runs the agent with message
// as the new prompt and blocks until that attempt completes, mirroring
// Spawn's foreground behavior (a continuation is always awaited by its
// caller in this package — background re-dispatch of a continuation is a
// caller-level concern, e.g. the workflow engine).
func (m *Manager) Continue(ctx context.Context, agentID, message string) (RunResult, error) {
	m.mu.Lock()
	n, ok := m.nodes[agentID]
	m.mu.Unlock()
	if !ok {
		return RunResult{}, fmt.Errorf("%w: %s", ErrNodeNotFound, agentID)
	}

	n.mu.Lock()
	if !n.status.isFinished() {
		st := n.status
		n.mu.Unlock()
		return RunResult{}, fmt.Errorf("%w: agent %s is %s", ErrNotFinished, agentID, st)
	}
	n.mu.Unlock()

	// A cancelled/closed edge is still resumable-by-continuation (spec
	// §2a): reopen it.
	if e, found, _ := m.graph.Edge(n.parent, agentID); found && e.Status == EdgeClosed {
		if err := m.graph.ReopenEdge(n.parent, agentID); err != nil {
			return RunResult{}, fmt.Errorf("subagent: reopen edge on continue: %w", err)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	n.mu.Lock()
	n.status = NodeRunning
	n.cancel = cancel
	n.done = make(chan struct{})
	n.mu.Unlock()

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
	done := n.done
	n.mu.Unlock()
	close(done)

	m.notify <- Notification{AgentID: agentID, ParentThread: n.parent, Status: status, Result: res, Err: err}
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
