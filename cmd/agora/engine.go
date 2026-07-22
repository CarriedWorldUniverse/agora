// engine.go: the ONE place every lane that runs a real turn engine
// (in-process TUI fallback, `agora pipe`, and each `agora daemon`-hosted
// thread) constructs a turnengine.Manager from. Before this file existed,
// inprocess.go built its Manager inline and daemon.go passed
// daemon.Config{} with no EngineFactory at all (agora-spec-io.md §0a/§1
// gap) — two lanes that could silently drift apart on store wiring,
// context extraction, etc. Centralizing the construction here is what
// keeps them from drifting; each lane supplies only what's genuinely
// lane-specific (its bridle.Provider, its contracts.ThreadStore, its
// toolrunner.Roots).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	bridle "github.com/CarriedWorldUniverse/bridle"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/daemon"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
	"github.com/CarriedWorldUniverse/agora/internal/subagent/enginerunner"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
)

// newTurnEngineManager builds the turnengine.Manager the SAME way for every
// real-engine lane: the caller's provider (claudesdk.New() in production,
// a bridle/fake.Provider in tests) over the caller's store and roots, with
// context extraction on (matching interactive sessions per the original
// inprocess.go rationale — U-E1). Exported-within-package so daemon.go,
// inprocess.go, and pipe.go all call through here instead of each building
// their own turnengine.NewManager call.
func newTurnEngineManager(threadID string, provider bridle.Provider, store contracts.ThreadStore, roots toolrunner.Roots, graph subagent.GraphStore) *turnengine.Manager {
	// Lifecycle hooks (internal/hooks via turnengine's HookRunner): discover
	// the operator's hooks.json (user + project layers) per engine
	// construction — here in the ONE shared seam so the TUI, pipe, and
	// daemon lanes all fire the same hooks. nil (no hooks.json anywhere,
	// the common case) costs nothing; discovery warnings are non-fatal
	// (stderr, never block a session).
	hookRunner, hookWarnings := turnengine.DiscoverHooks(roots.WorkingDir)
	for _, w := range hookWarnings {
		fmt.Fprintln(os.Stderr, w)
	}
	return turnengine.NewManager(threadID, provider,
		turnengine.WithRoots(roots),
		turnengine.WithStore(store),
		// Interactive sessions distill each turn into durable facts using the
		// active model itself (ctxmap fact extraction) — off by default in the
		// engine, on here, for every lane that runs a real turn.
		turnengine.WithContextExtraction(true),
		turnengine.WithHooks(hookRunner),
		// agent() delegation (subagents spec §2): the PARENT Manager gets the
		// tool; enginerunner never re-wires it onto children (structural
		// depth guard — see turnengine.Manager.subagents' doc comment).
		turnengine.WithSubagents(subagent.NewManager(store, graph, subagent.NewRegistry(nil),
			enginerunner.New(provider, store))),
	)
}

// openAgentGraph opens the ONE process-wide agent-graph store every engine
// this process builds shares: the JSONL log at ~/.agora/agent-graph.jsonl
// (edges must survive restarts like the child threads they describe; spec
// §3). Exactly one open file handle per process, owned by the composition
// root that called this — the returned closeFn MUST be called at shutdown
// (Windows file locking turns a leaked handle into a hard failure, which
// is how the per-Manager open this replaces was caught). An open failure
// degrades to the in-memory store with a stderr warning — delegation still
// works, the graph just won't persist (never-fail-the-session posture).
func openAgentGraph() (graph subagent.GraphStore, closeFn func()) {
	fg, err := subagent.OpenFileGraphStore(filepath.Join(userHomeOrDot(), ".agora", "agent-graph.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "agora: open agent graph store: %v (agent graph will not persist this session)\n", err)
		return subagent.NewMemGraphStore(), func() {}
	}
	return fg, func() {
		if cerr := fg.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "agora: close agent graph store: %v\n", cerr)
		}
	}
}

// newEngineFactory builds a daemon.EngineFactory: the per-thread seam
// internal/daemon.Daemon calls the first time a thread's Session is minted
// (registry.go's Session method). It resolves toolrunner.Roots from the
// thread's persisted meta.WorkingDir (a daemon-hosted thread has no
// process cwd of its own — each thread can be about a different
// directory, per agora-spec-io.md §3a) and builds a Manager over provider/
// store via newTurnEngineManager.
//
// A Roots-construction failure (a malformed/unreachable WorkingDir) can't
// be returned here — EngineFactory's signature has no error return — so it
// is surfaced as an errEngine: the thread's very first turn fails
// immediately with the real error instead of the daemon panicking or
// silently no-opping.
func newEngineFactory(provider bridle.Provider, store contracts.ThreadStore, graph subagent.GraphStore) daemon.EngineFactory {
	return func(threadID string, meta contracts.ThreadMeta) agoraio.Engine {
		roots, err := toolrunner.NewRoots(meta.WorkingDir)
		if err != nil {
			return errEngine{err: fmt.Errorf("daemon: build roots for thread %q (working_dir %q): %w", threadID, meta.WorkingDir, err)}
		}
		return newTurnEngineManager(threadID, provider, store, roots, graph)
	}
}

// newInProcessManager builds the SAME real-engine construction the
// standalone (no-daemon) lanes share: a persistent LocalStore rooted at
// the operator's ~/.agora state dir (newInProcessStore), the calling
// process's cwd as the thread's roots, the thread created in the store if
// it doesn't already exist, and a Manager over provider via
// newTurnEngineManager. Both newInProcessBackend (the TUI's no-daemon
// fallback) and runPipe (`agora pipe`) call this so they can't drift on
// how a standalone thread is stood up; the two differ only in what they do
// with the resulting Manager/store (wrap in a tui.Backend vs. hand
// straight to agoraio.RunPipe as an Engine).
// The returned cleanup closes process-wide resources this construction
// opened (the agent-graph file handle) — callers MUST invoke it at
// shutdown (backend Close / pipe exit).
func newInProcessManager(threadID string, provider bridle.Provider) (*turnengine.Manager, contracts.ThreadStore, func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("inprocess: getwd: %w", err)
	}
	roots, err := toolrunner.NewRoots(cwd)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("inprocess: build roots for %q: %w", cwd, err)
	}

	store, err := newInProcessStore()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("inprocess: open thread store: %w", err)
	}
	if err := ensureThreadCreated(store, threadID, roots.WorkingDir); err != nil {
		return nil, nil, nil, fmt.Errorf("inprocess: create thread %q: %w", threadID, err)
	}

	graph, closeGraph := openAgentGraph()
	return newTurnEngineManager(threadID, provider, store, roots, graph), store, closeGraph, nil
}

// errEngine is an agoraio.Engine that fails immediately with a fixed error
// — the EngineFactory failure path above, and useful anywhere else a
// construction error needs to surface through the Engine seam instead of a
// Go error return that isn't available.
type errEngine struct{ err error }

func (e errEngine) Run(_ context.Context, _ <-chan contracts.Input, out chan<- contracts.Event) error {
	close(out)
	return e.err
}
