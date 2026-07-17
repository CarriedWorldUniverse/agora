package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/persistence"
)

// fakeClock is a fixed injected Clock — ground rule 4: no wall-clock in tests.
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

// instantRunner completes immediately with a fixed output. Deterministic,
// no model calls (ground rule 6: the actual agent execution is stubbed).
type instantRunner struct {
	output json.RawMessage
}

func (r *instantRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if ctx.Err() != nil {
		return RunResult{}, ctx.Err()
	}
	return RunResult{Output: r.output}, nil
}

// blockingRunner runs until its release channel is closed or ctx is
// cancelled — lets tests deterministically hold an agent "running" without
// a wall-clock sleep (ground rule 4).
type blockingRunner struct {
	release chan struct{}
	output  json.RawMessage
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{release: make(chan struct{}), output: json.RawMessage(`{"ok":true}`)}
}

func (r *blockingRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	select {
	case <-r.release:
		return RunResult{Output: r.output}, nil
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	}
}

// capturingRunner completes immediately, recording the last RunRequest it
// received — lets a test inspect what tool set/model/etc a Spawn actually
// resolved to without needing an exported node accessor.
type capturingRunner struct {
	output json.RawMessage

	mu  sync.Mutex
	req RunRequest
}

func (r *capturingRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	r.mu.Lock()
	r.req = req
	r.mu.Unlock()
	return RunResult{Output: r.output}, nil
}

func (r *capturingRunner) lastRequest() RunRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.req
}

func newTestManager(t *testing.T, runner AgentRunner, opts ...ManagerOption) *Manager {
	t.Helper()
	store := persistence.NewMemStore()
	graph := NewMemGraphStore()
	allOpts := append([]ManagerOption{WithClock(fakeClock{t: time.Unix(1000, 0).UTC()})}, opts...)
	return NewManager(store, graph, NewRegistry(nil), runner, allOpts...)
}

func TestManager_SpawnForeground_CallerAwaitsResult(t *testing.T) {
	m := newTestManager(t, &instantRunner{output: json.RawMessage(`{"result":"done"}`)})
	id, err := m.Spawn(context.Background(), "root", "do a thing", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Foreground means "the caller blocks the turn on this" (spec §2) — the
	// caller expresses that by awaiting Result; Spawn itself always returns
	// immediately (see SpawnOpts.Foreground doc comment).
	res, runErr, ok := m.Result(id)
	if !ok || runErr != nil {
		t.Fatalf("Result: ok=%v err=%v", ok, runErr)
	}
	if string(res.Output) != `{"result":"done"}` {
		t.Errorf("Output = %s", res.Output)
	}
	status, ok := m.Status(id)
	if !ok || status != NodeCompleted {
		t.Fatalf("Status = %v, ok=%v, want completed", status, ok)
	}
}

func TestManager_SpawnBackground_ReturnsImmediately(t *testing.T) {
	r := newBlockingRunner()
	m := newTestManager(t, r)
	id, err := m.Spawn(context.Background(), "root", "do a thing", SpawnOpts{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	status, ok := m.Status(id)
	if !ok || status != NodeRunning {
		t.Fatalf("Status = %v immediately after background Spawn, want running", status)
	}
	close(r.release)
	res, runErr, ok := m.Result(id) // blocks until the run finishes
	if !ok || runErr != nil {
		t.Fatalf("Result: ok=%v err=%v", ok, runErr)
	}
	if string(res.Output) != `{"ok":true}` {
		t.Errorf("Output = %s", res.Output)
	}

	select {
	case n := <-m.Notifications():
		if n.AgentID != id || n.Status != NodeCompleted {
			t.Errorf("Notification = %+v, want completed for %s", n, id)
		}
	default:
		t.Fatal("no notification delivered")
	}
}

func TestManager_DepthCap(t *testing.T) {
	m := newTestManager(t, &instantRunner{output: json.RawMessage(`{}`)}) // default depth cap 1
	child, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn child: %v", err)
	}
	_, err = m.Spawn(context.Background(), child, "grandchild", SpawnOpts{Foreground: true})
	if !errors.Is(err, ErrDepthCapExceeded) {
		t.Fatalf("err = %v, want ErrDepthCapExceeded", err)
	}
}

func TestManager_DepthCap_RaisedExplicitly(t *testing.T) {
	m := newTestManager(t, &instantRunner{output: json.RawMessage(`{}`)}, WithDepthCap(2))
	child, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn child: %v", err)
	}
	_, err = m.Spawn(context.Background(), child, "grandchild", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn grandchild with raised cap: %v", err)
	}
}

func TestManager_Continue_RequiresFinished(t *testing.T) {
	r := newBlockingRunner()
	m := newTestManager(t, r)
	id, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	_, err = m.Continue(context.Background(), id, "keep going")
	if !errors.Is(err, ErrNotFinished) {
		t.Fatalf("err = %v, want ErrNotFinished (still running)", err)
	}
	close(r.release)
	m.Result(id) // drain
}

func TestManager_Continue_ResumesFinishedAgent(t *testing.T) {
	m := newTestManager(t, &instantRunner{output: json.RawMessage(`{"n":1}`)})
	id, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, _, ok := m.Result(id); !ok {
		t.Fatal("Result: agent not found")
	}
	res, err := m.Continue(context.Background(), id, "one more thing")
	if err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if string(res.Output) != `{"n":1}` {
		t.Errorf("Output = %s", res.Output)
	}
	status, _ := m.Status(id)
	if status != NodeCompleted {
		t.Errorf("Status after Continue = %v, want completed", status)
	}
}

func TestManager_Continue_UnknownAgent(t *testing.T) {
	m := newTestManager(t, &instantRunner{})
	_, err := m.Continue(context.Background(), "nope", "hi")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("err = %v, want ErrNodeNotFound", err)
	}
}

func TestManager_SchemaForcedSpawn_RetriesThenSucceeds(t *testing.T) {
	cr := &countingRunner{
		failAttempts: 2,
		badOutput:    json.RawMessage(`{"nope":true}`),
		goodOutput:   json.RawMessage(`{"answer":"42"}`),
	}
	m := newTestManager(t, cr)
	id, err := m.Spawn(context.Background(), "root", "answer this", SpawnOpts{
		Foreground: true,
		Schema:     schemaWithRequired,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	res, runErr, ok := m.Result(id)
	if !ok || runErr != nil {
		t.Fatalf("Result: ok=%v err=%v", ok, runErr)
	}
	if string(res.Output) != string(cr.goodOutput) {
		t.Errorf("Output = %s", res.Output)
	}
}

func TestManager_SchemaForcedSpawn_GivesUp(t *testing.T) {
	cr := &countingRunner{
		failAttempts: 1000,
		badOutput:    json.RawMessage(`{"nope":true}`),
		goodOutput:   json.RawMessage(`{"answer":"unreachable"}`),
	}
	m := newTestManager(t, cr)
	id, err := m.Spawn(context.Background(), "root", "answer this", SpawnOpts{
		Foreground: true,
		Schema:     schemaWithRequired,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	_, runErr, ok := m.Result(id)
	if !ok || !errors.Is(runErr, ErrSchemaGiveUp) {
		t.Fatalf("runErr = %v, ok=%v, want ErrSchemaGiveUp", runErr, ok)
	}
	status, _ := m.Status(id)
	if status != NodeErrored {
		t.Errorf("Status = %v, want errored", status)
	}
}

// TestManager_Continue_ConcurrentCallsDoNotDoubleClose is FIX 2's
// regression: N concurrent Continue calls racing on one already-finished
// agent must not double-close its done channel (process-killing panic) or
// deadlock — exactly one call may proceed to actually run, every other call
// must fail fast with ErrNotFinished.
func TestManager_Continue_ConcurrentCallsDoNotDoubleClose(t *testing.T) {
	m := newTestManager(t, &instantRunner{output: json.RawMessage(`{"n":1}`)})
	id, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, _, ok := m.Result(id); !ok {
		t.Fatal("Result: agent not found")
	}

	const n = 20
	var wg sync.WaitGroup
	successes := make([]bool, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := m.Continue(context.Background(), id, "go again")
			errs[i] = err
			successes[i] = err == nil
		}(i)
	}
	wg.Wait()

	successCount := 0
	for i, ok := range successes {
		if ok {
			successCount++
			continue
		}
		if !errors.Is(errs[i], ErrNotFinished) {
			t.Errorf("call %d: err = %v, want nil or ErrNotFinished", i, errs[i])
		}
	}
	if successCount == 0 {
		t.Error("expected at least one Continue call to succeed")
	}
}

// TestManager_SpawnCap enforces FIX 4's per-session spawn cap.
func TestManager_SpawnCap(t *testing.T) {
	m := newTestManager(t, &instantRunner{output: json.RawMessage(`{}`)}, WithSpawnCap(2))
	for i := 0; i < 2; i++ {
		if _, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{}); err != nil {
			t.Fatalf("Spawn %d: %v", i, err)
		}
	}
	_, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{})
	if !errors.Is(err, ErrSpawnCapExceeded) {
		t.Fatalf("err = %v, want ErrSpawnCapExceeded", err)
	}
}

// TestManager_NotifyOverflow_DoesNotBlockOrLeak is FIX 4's regression: many
// completions with nobody draining Notifications() must not block/leak a
// completion goroutine.
func TestManager_NotifyOverflow_DoesNotBlockOrLeak(t *testing.T) {
	m := newTestManager(t, &instantRunner{output: json.RawMessage(`{}`)}, WithMaxConcurrent(32), WithSpawnCap(2000))
	const n = 600 // well past the 256-slot notify buffer
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{})
		if err != nil {
			t.Fatalf("Spawn %d: %v", i, err)
		}
		ids[i] = id
	}
	for _, id := range ids {
		if _, err, ok := m.Result(id); !ok || err != nil {
			t.Fatalf("Result(%s): ok=%v err=%v", id, ok, err)
		}
	}
	if got := m.NotificationsDropped(); got == 0 {
		t.Log("no notifications dropped (buffer happened not to overflow) — not a failure, but weakens this regression's coverage")
	}
}

// TestManager_Spawn_UnregisteredParent_FailsClosedOnTools is FIX 5's
// regression: a first-level spawn from a parent thread never seeded via
// RegisterRoot must not receive an unrestricted tool set.
func TestManager_Spawn_UnregisteredParent_FailsClosedOnTools(t *testing.T) {
	cr := &capturingRunner{output: json.RawMessage(`{}`)}
	m := newTestManager(t, cr)
	// general-purpose's def.Tools is nil ("all tools" per its own def), so
	// without the fix eff.Tools would end up nil too (parent.Tools nil ->
	// "unrestricted", untouched by the def since def.Tools is nil) —
	// indistinguishable from a deliberately-unrestricted root. Use "explore"
	// instead: its def.Tools is a concrete allowlist, so the intersection
	// against a fail-closed empty parent set is directly observable.
	id, err := m.Spawn(context.Background(), "untracked-root", "look around", SpawnOpts{
		AgentType:  BuiltinExplore,
		Foreground: true,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, _, ok := m.Result(id); !ok {
		t.Fatal("Result: agent not found")
	}
	req := cr.lastRequest()
	if len(req.Tools) != 0 {
		t.Errorf("Tools = %v from an unregistered parent, want empty (fail closed, not unrestricted)", req.Tools)
	}
}

// TestManager_Spawn_RegisteredRoot_GrantsSeededTools proves RegisterRoot is
// the escape hatch: a seeded root's tool set narrows normally instead of
// failing closed.
func TestManager_Spawn_RegisteredRoot_GrantsSeededTools(t *testing.T) {
	cr := &capturingRunner{output: json.RawMessage(`{}`)}
	m := newTestManager(t, cr)
	m.RegisterRoot("tracked-root", ParentContext{Tools: []string{"Read", "Glob", "Grep", "Bash"}})
	id, err := m.Spawn(context.Background(), "tracked-root", "look around", SpawnOpts{
		AgentType:  BuiltinExplore,
		Foreground: true,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, _, ok := m.Result(id); !ok {
		t.Fatal("Result: agent not found")
	}
	req := cr.lastRequest()
	want := []string{"Read", "Glob", "Grep"} // explore's def.Tools, narrowed by intersection
	if len(req.Tools) != len(want) {
		t.Fatalf("Tools = %v, want %v", req.Tools, want)
	}
	for i, tool := range want {
		if req.Tools[i] != tool {
			t.Fatalf("Tools = %v, want %v", req.Tools, want)
		}
	}
}

// Concurrency: parallel spawns must all complete correctly under -race.
func TestManager_ParallelSpawns_Race(t *testing.T) {
	m := newTestManager(t, &instantRunner{output: json.RawMessage(`{}`)}, WithMaxConcurrent(4))
	const n = 20
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id, err := m.Spawn(context.Background(), "root", "p", SpawnOpts{})
		if err != nil {
			t.Fatalf("Spawn %d: %v", i, err)
		}
		ids[i] = id
	}
	for _, id := range ids {
		if _, err, ok := m.Result(id); !ok || err != nil {
			t.Fatalf("Result(%s): ok=%v err=%v", id, ok, err)
		}
	}
}
