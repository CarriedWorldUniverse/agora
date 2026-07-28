package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/persistence"
)

// failingRunner always returns an error, for the errored-outcome path.
type failingRunner struct{ err error }

func (r *failingRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	return RunResult{}, r.err
}

var testTS = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// TestRecordOutcome_RejectsRunning is the honesty property behind agora#158:
// a persisted "running" becomes a lie the instant the process dies, and a
// reload would report children as live that nothing is executing. Absence of
// an outcome — not a stored "running" — is what represents an unfinished run.
func TestRecordOutcome_RejectsRunning(t *testing.T) {
	g := NewMemGraphStore()
	if err := g.AddEdge(Edge{ParentThread: "p", ChildThread: "c", Status: EdgeOpen, CreatedAt: testTS}); err != nil {
		t.Fatal(err)
	}
	err := g.RecordOutcome("p", "c", NodeRunning, testTS)
	if !errors.Is(err, ErrNonTerminalOutcome) {
		t.Fatalf("RecordOutcome(running) error = %v; want ErrNonTerminalOutcome — persisting \"running\" is a lie after a crash", err)
	}
	e, _, _ := g.Edge("p", "c")
	if e.Outcome != "" {
		t.Errorf("outcome = %q; want empty after a rejected write", e.Outcome)
	}
}

func TestRecordOutcome_TerminalStatusesRoundTrip(t *testing.T) {
	for _, st := range []NodeStatus{NodeCompleted, NodeErrored, NodeInterrupted} {
		g := NewMemGraphStore()
		if err := g.AddEdge(Edge{ParentThread: "p", ChildThread: "c", Status: EdgeOpen, CreatedAt: testTS}); err != nil {
			t.Fatal(err)
		}
		if err := g.RecordOutcome("p", "c", st, testTS); err != nil {
			t.Fatalf("RecordOutcome(%s): %v", st, err)
		}
		e, ok, _ := g.Edge("p", "c")
		if !ok || e.Outcome != st {
			t.Errorf("outcome = %q; want %q", e.Outcome, st)
		}
		if e.FinishedAt.IsZero() {
			t.Errorf("%s: FinishedAt not set", st)
		}
		// The edge must STAY OPEN: a finished child is
		// resumable-by-continuation, and closing hides its subtree. This is
		// the distinction agora#154 got wrong.
		if e.Status != EdgeOpen {
			t.Errorf("%s: edge status = %s; want open — outcome and shape are orthogonal", st, e.Status)
		}
	}
}

func TestRecordOutcome_UnknownEdge(t *testing.T) {
	g := NewMemGraphStore()
	if err := g.RecordOutcome("p", "nope", NodeCompleted, testTS); !errors.Is(err, ErrEdgeNotFound) {
		t.Fatalf("error = %v; want ErrEdgeNotFound", err)
	}
}

// TestFileGraphStore_OutcomeSurvivesReload is the point of the whole change:
// after the process that ran a child exits, something must still be able to
// say whether it finished. Before this, completed, errored and
// abandoned-mid-run were indistinguishable on disk.
func TestFileGraphStore_OutcomeSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.jsonl")
	g, err := OpenFileGraphStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"done", "failed", "abandoned"} {
		if err := g.AddEdge(Edge{ParentThread: "p", ChildThread: c, Status: EdgeOpen, CreatedAt: testTS}); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.RecordOutcome("p", "done", NodeCompleted, testTS); err != nil {
		t.Fatal(err)
	}
	if err := g.RecordOutcome("p", "failed", NodeErrored, testTS); err != nil {
		t.Fatal(err)
	}
	// "abandoned" gets no outcome — the process died mid-run.
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := OpenFileGraphStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()

	want := map[string]NodeStatus{"done": NodeCompleted, "failed": NodeErrored, "abandoned": ""}
	kids, err := reloaded.Children("p", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 3 {
		t.Fatalf("got %d children after reload; want 3", len(kids))
	}
	for _, e := range kids {
		if got := e.Outcome; got != want[e.ChildThread] {
			t.Errorf("%s: outcome after reload = %q; want %q", e.ChildThread, got, want[e.ChildThread])
		}
	}
	// And the distinction that was previously impossible: which of these is
	// unfinished?
	var unfinished []string
	for _, e := range kids {
		if e.Outcome == "" {
			unfinished = append(unfinished, e.ChildThread)
		}
	}
	if len(unfinished) != 1 || unfinished[0] != "abandoned" {
		t.Errorf("unfinished = %v; want exactly [abandoned] — this is the question the graph could not answer at all (agora#158)", unfinished)
	}
}

// newOutcomeManager mirrors newTestManager but keeps a handle on the graph so
// the test can read back what the run recorded.
func newOutcomeManager(t *testing.T, runner AgentRunner) (*Manager, *MemGraphStore) {
	t.Helper()
	graph := NewMemGraphStore()
	m := NewManager(persistence.NewMemStore(), graph, NewRegistry(nil), runner,
		WithClock(fakeClock{t: testTS}))
	return m, graph
}

// TestManager_RunRecordsOutcomeOnTheGraph is the end-to-end half: a real
// spawn must leave a durable record of how it ended. Asserting only on
// Manager.Status would prove nothing — that is the in-memory value that
// already worked and that vanishes with the process.
func TestManager_RunRecordsOutcomeOnTheGraph(t *testing.T) {
	m, graph := newOutcomeManager(t, &instantRunner{output: json.RawMessage(`{"result":"done"}`)})
	id, err := m.Spawn(context.Background(), "root", "do a thing", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, _, ok := m.Result(id); !ok {
		t.Fatal("Result not ok")
	}

	e, ok, err := graph.Edge("root", id)
	if err != nil || !ok {
		t.Fatalf("Edge(root,%s): ok=%v err=%v", id, ok, err)
	}
	if e.Outcome != NodeCompleted {
		t.Errorf("graph outcome = %q; want %q — without this nothing can say the child finished once the process exits (agora#158)", e.Outcome, NodeCompleted)
	}
	if e.FinishedAt.IsZero() {
		t.Error("FinishedAt not recorded")
	}
	// Shape stays untouched: a finished child is resumable-by-continuation.
	if e.Status != EdgeOpen {
		t.Errorf("edge status = %s; want open", e.Status)
	}
}

// TestManager_RunRecordsErroredOutcome covers the failure path — the one an
// operator most wants to find after the fact.
func TestManager_RunRecordsErroredOutcome(t *testing.T) {
	m, graph := newOutcomeManager(t, &failingRunner{err: errors.New("boom")})
	id, err := m.Spawn(context.Background(), "root", "do a thing", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, runErr, _ := m.Result(id); runErr == nil {
		t.Fatal("want a run error")
	}
	e, ok, _ := graph.Edge("root", id)
	if !ok || e.Outcome != NodeErrored {
		t.Errorf("graph outcome = %q; want %q", e.Outcome, NodeErrored)
	}
}
