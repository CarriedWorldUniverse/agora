package subagent

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// EdgeStatus is a graph edge's lifecycle state.
// Spec: agora-spec-subagents.md §3.
type EdgeStatus string

const (
	// EdgeOpen: the child is a live part of the graph (BFS traverses it,
	// cancellation propagates through it).
	EdgeOpen EdgeStatus = "open"
	// EdgeClosed: hides the subtree — spec §3: "a closed edge hides its
	// subtree". Set when a node is cancelled/torn down.
	EdgeClosed EdgeStatus = "closed"
)

// Edge is one parent→child spawn edge in the persisted agent graph.
// Spec: agora-spec-subagents.md §3: "persist parent/child edges
// (parent_thread, child_thread, status: open|closed)".
type Edge struct {
	ParentThread string     `json:"parent_thread"`
	ChildThread  string     `json:"child_thread"`
	Status       EdgeStatus `json:"status"`
	// Foreground records whether the spawn was run_in_background:false — the
	// cancellation matrix (§2a) needs this to distinguish "cancelled with
	// the turn" children from background ones.
	Foreground bool      `json:"foreground"`
	CreatedAt  time.Time `json:"created_at"`
	// Outcome is the child's TERMINAL run status, or "" if none was ever
	// recorded (agora#158). Distinct from Status, which is the graph-SHAPE
	// view: an edge stays open after a normal finish because the child is
	// resumable-by-continuation, so Status cannot tell you whether anything
	// is still running.
	//
	// NodeRunning is deliberately NEVER persisted here. A stored "running"
	// becomes a lie the instant the process dies, and reload would report
	// children as live that nothing is executing. So the absence of an
	// outcome IS the non-terminal state: in a fresh process it means
	// abandoned, since no run survives a restart. That keeps the record
	// honest without needing a liveness protocol.
	Outcome NodeStatus `json:"outcome,omitempty"`
	// FinishedAt is when Outcome was recorded; zero while Outcome is "".
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// GraphStore is the storage-neutral seam for the agent graph — parallels
// contracts.ThreadStore's role for threads (spec §3: "This is codex's
// agent-graph-store — tiny and worth having from day one"). Two
// implementations ship: MemGraphStore (tests, ephemeral) and
// FileGraphStore (JSONL-persisted, reload via replay — the same
// append+replay shape internal/persistence uses for threads, sized to this
// package's much smaller need).
type GraphStore interface {
	// AddEdge records a new parent→child spawn, status open.
	// Returns ErrEdgeExists if the pair is already recorded.
	AddEdge(e Edge) error
	// CloseEdge marks an edge closed (hides its subtree). Idempotent: closing
	// an already-closed edge is a no-op. Returns ErrEdgeNotFound if the pair
	// was never recorded.
	CloseEdge(parent, child string) error
	// ReopenEdge marks a closed edge open again — spec §2a: "a cancelled
	// child is resumable-by-continuation like any finished agent". Returns
	// ErrEdgeNotFound if the pair was never recorded.
	ReopenEdge(parent, child string) error
	// RecordOutcome stores a child's terminal run status (agora#158).
	// Rejects a non-terminal status with ErrNonTerminalOutcome so
	// NodeRunning can never be persisted by mistake — see Edge.Outcome for
	// why absence, not a stored "running", represents an unfinished run.
	// Last write wins; returns ErrEdgeNotFound if the pair is unknown.
	RecordOutcome(parent, child string, status NodeStatus, ts time.Time) error
	// Edge looks up one edge by (parent,child).
	Edge(parent, child string) (Edge, bool, error)
	// Children returns parent's direct children, deterministic order
	// (ChildThread ascending). openOnly filters to EdgeOpen edges.
	Children(parent string, openOnly bool) ([]Edge, error)
	// Descendants returns the BFS-ordered set of edges reachable from root
	// (root's children, their children, ...), deterministic within each BFS
	// level (ChildThread ascending). openOnly stops traversal at (and
	// excludes) closed edges — spec §3: "a closed edge hides its subtree".
	Descendants(root string, openOnly bool) ([]Edge, error)
}

// MemGraphStore is a pure in-memory GraphStore — tests and ephemeral pods.
type MemGraphStore struct {
	mu sync.Mutex
	// byParent[parent][child] = edge.
	byParent map[string]map[string]Edge
}

// NewMemGraphStore returns an empty MemGraphStore.
func NewMemGraphStore() *MemGraphStore {
	return &MemGraphStore{byParent: make(map[string]map[string]Edge)}
}

var _ GraphStore = (*MemGraphStore)(nil)

func (g *MemGraphStore) AddEdge(e Edge) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.addEdgeLocked(e)
}

func (g *MemGraphStore) addEdgeLocked(e Edge) error {
	children, ok := g.byParent[e.ParentThread]
	if !ok {
		children = make(map[string]Edge)
		g.byParent[e.ParentThread] = children
	}
	if _, exists := children[e.ChildThread]; exists {
		return fmt.Errorf("%w: %s -> %s", ErrEdgeExists, e.ParentThread, e.ChildThread)
	}
	if e.Status == "" {
		e.Status = EdgeOpen
	}
	children[e.ChildThread] = e
	return nil
}

func (g *MemGraphStore) RecordOutcome(parent, child string, status NodeStatus, ts time.Time) error {
	if !status.isFinished() {
		return fmt.Errorf("%w: %s", ErrNonTerminalOutcome, status)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	children, ok := g.byParent[parent]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrEdgeNotFound, parent, child)
	}
	e, ok := children[child]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrEdgeNotFound, parent, child)
	}
	e.Outcome = status
	e.FinishedAt = ts.UTC()
	children[child] = e
	return nil
}

func (g *MemGraphStore) CloseEdge(parent, child string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closeEdgeLocked(parent, child)
}

func (g *MemGraphStore) closeEdgeLocked(parent, child string) error {
	children, ok := g.byParent[parent]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrEdgeNotFound, parent, child)
	}
	e, ok := children[child]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrEdgeNotFound, parent, child)
	}
	e.Status = EdgeClosed
	children[child] = e
	return nil
}

func (g *MemGraphStore) ReopenEdge(parent, child string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reopenEdgeLocked(parent, child)
}

func (g *MemGraphStore) reopenEdgeLocked(parent, child string) error {
	children, ok := g.byParent[parent]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrEdgeNotFound, parent, child)
	}
	e, ok := children[child]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrEdgeNotFound, parent, child)
	}
	e.Status = EdgeOpen
	children[child] = e
	return nil
}

func (g *MemGraphStore) Edge(parent, child string) (Edge, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	children, ok := g.byParent[parent]
	if !ok {
		return Edge{}, false, nil
	}
	e, ok := children[child]
	return e, ok, nil
}

func (g *MemGraphStore) Children(parent string, openOnly bool) ([]Edge, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.childrenLocked(parent, openOnly), nil
}

func (g *MemGraphStore) childrenLocked(parent string, openOnly bool) []Edge {
	children := g.byParent[parent]
	out := make([]Edge, 0, len(children))
	for _, e := range children {
		if openOnly && e.Status != EdgeOpen {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChildThread < out[j].ChildThread })
	return out
}

func (g *MemGraphStore) Descendants(root string, openOnly bool) ([]Edge, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return bfs(root, openOnly, g.childrenLocked), nil
}

// bfs walks the graph breadth-first from root using childrenOf (already
// deterministically sorted per level by the caller's childrenLocked), and
// returns the visited edges in level order — the shared traversal both
// GraphStore implementations use.
func bfs(root string, openOnly bool, childrenOf func(parent string, openOnly bool) []Edge) []Edge {
	var out []Edge
	visited := map[string]bool{root: true}
	queue := []string{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, e := range childrenOf(parent, openOnly) {
			if visited[e.ChildThread] {
				continue // defensive: a well-formed graph is a forest, but never loop
			}
			visited[e.ChildThread] = true
			out = append(out, e)
			queue = append(queue, e.ChildThread)
		}
	}
	return out
}
