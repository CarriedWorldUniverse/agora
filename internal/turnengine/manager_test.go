package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

const testTimeout = 5 * time.Second

func recvWithin(t *testing.T, ch <-chan contracts.Event, d time.Duration) contracts.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while waiting for event")
		}
		return ev
	case <-time.After(d):
		t.Fatal("timed out waiting for event")
		return contracts.Event{}
	}
}

func expectClosed(t *testing.T, ch <-chan contracts.Event, d time.Duration) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("expected out to be closed, got event %+v", ev)
		}
	case <-time.After(d):
		t.Fatal("timed out waiting for out to close")
	}
}

// TestManager_OneTextTurn drives the exact slice this ticket proves: a
// user_message flows through bridle's fake provider and comes out as
// turn.started, an item.*{agent_message} carrying the text, then
// turn.completed with the right turn_id and Usage.
func TestManager_OneTextTurn(t *testing.T) {
	provider := fake.NewProvider(fake.Step{
		Text:  "hello from the fake",
		Usage: bridle.Usage{InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 2, ReasoningTokens: 1},
	})
	m := NewManager("th_test", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 8)

	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}

	started := recvWithin(t, out, testTimeout)
	if started.Type != contracts.EvTurnStarted {
		t.Fatalf("first event = %+v; want turn.started", started)
	}
	if started.ThreadID != "th_test" || started.TurnID != "tu_0001" {
		t.Fatalf("turn.started ids = %q/%q; want th_test/tu_0001", started.ThreadID, started.TurnID)
	}

	itemStarted := recvWithin(t, out, testTimeout)
	if itemStarted.Type != contracts.EvItemStarted {
		t.Fatalf("second event = %+v; want item.started", itemStarted)
	}
	if itemStarted.Item == nil || itemStarted.Item.Type != contracts.ItemAgentMessage || itemStarted.Item.Seq != 1 {
		t.Fatalf("item.started Item = %+v; want {seq:1 type:agent_message}", itemStarted.Item)
	}

	delta := recvWithin(t, out, testTimeout)
	if delta.Type != contracts.EvAgentMessageDelta {
		t.Fatalf("third event = %+v; want item.agent_message.delta", delta)
	}
	var deltaPayload itemPayload
	if err := json.Unmarshal(delta.Payload, &deltaPayload); err != nil {
		t.Fatalf("decode delta payload: %v", err)
	}
	if deltaPayload.Text != "hello from the fake" {
		t.Fatalf("delta text = %q; want %q", deltaPayload.Text, "hello from the fake")
	}

	completed := recvWithin(t, out, testTimeout)
	if completed.Type != contracts.EvItemCompleted {
		t.Fatalf("fourth event = %+v; want item.completed", completed)
	}
	var completedPayload itemPayload
	if err := json.Unmarshal(completed.Payload, &completedPayload); err != nil {
		t.Fatalf("decode item.completed payload: %v", err)
	}
	if completedPayload.Text != "hello from the fake" {
		t.Fatalf("item.completed text = %q; want %q", completedPayload.Text, "hello from the fake")
	}

	turnCompleted := recvWithin(t, out, testTimeout)
	if turnCompleted.Type != contracts.EvTurnCompleted {
		t.Fatalf("fifth event = %+v; want turn.completed", turnCompleted)
	}
	if turnCompleted.TurnID != "tu_0001" {
		t.Fatalf("turn.completed turn_id = %q; want tu_0001", turnCompleted.TurnID)
	}
	var usage usagePayload
	if err := json.Unmarshal(turnCompleted.Payload, &usage); err != nil {
		t.Fatalf("decode usage payload: %v", err)
	}
	want := contracts.Usage{Input: 10, Output: 5, Cached: 2, Reasoning: 1}
	if usage.Usage != want {
		t.Fatalf("usage = %+v; want %+v", usage.Usage, want)
	}

	// Engine contract: end closes out and Run returns nil.
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}

// TestManager_EngineContract_CtxDoneClosesOut asserts the io.Engine
// contract directly (internal/io/engine.go: "Run MUST close out before
// returning"): canceling ctx while idle (no turn in flight) closes out
// and Run returns ctx.Err().
func TestManager_EngineContract_CtxDoneClosesOut(t *testing.T) {
	m := NewManager("th_test", fake.NewProvider())
	in := make(chan contracts.Input)
	out := make(chan contracts.Event)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(ctx, in, out) }()

	cancel()
	expectClosed(t, out, testTimeout)
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("Run returned %v; want context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// TestManager_EngineContract_InClosingEndsRun: closing in (no explicit
// end Input) also winds Run down and closes out — the other half of
// engine.go's "consumes Input from in until in is closed or ctx is
// canceled" contract.
func TestManager_EngineContract_InClosingEndsRun(t *testing.T) {
	m := NewManager("th_test", fake.NewProvider())
	in := make(chan contracts.Input)
	out := make(chan contracts.Event)

	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	close(in)
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}

// TestManager_MultiTurn_SecondMessageAfterCompletion drives a full turn to
// turn.completed, then sends a second user_message immediately after
// observing that turn.completed on out (no synchronization wait) —
// deliberately racing Run's select loop: the just-finished turn's
// `<-turnDone` reap case and the buffered `<-in` case are both ready at
// roughly the same time, so Go's pseudo-random select may service `in`
// FIRST. Before the FIX 2 reap-in-InUserMessage guard, that ordering hit
// the "turn already in flight" branch (turnCancel was still non-nil) and
// silently DROPPED the second message — a message that arrived AFTER
// completion and should start a brand new turn, not be mistaken for a
// mid-turn steer. Run under -race -count=10 (per the review gate) to give
// the scheduler a real chance to hit the losing order repeatedly.
func TestManager_MultiTurn_SecondMessageAfterCompletion(t *testing.T) {
	provider := fake.NewProvider(
		fake.Step{Text: "first"},
		fake.Step{Text: "second"},
	)
	m := NewManager("th_multi", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001", "tu_0002"}}))

	in := make(chan contracts.Input, 2)
	out := make(chan contracts.Event, 16)

	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "one"}

	ev := recvWithin(t, out, testTimeout) // turn.started
	if ev.Type != contracts.EvTurnStarted || ev.TurnID != "tu_0001" {
		t.Fatalf("turn 1 start = %+v; want turn.started/tu_0001", ev)
	}
	recvWithin(t, out, testTimeout) // item.started
	recvWithin(t, out, testTimeout) // item.agent_message.delta
	recvWithin(t, out, testTimeout) // item.completed
	ev = recvWithin(t, out, testTimeout)
	if ev.Type != contracts.EvTurnCompleted || ev.TurnID != "tu_0001" {
		t.Fatalf("turn 1 completed = %+v; want turn.completed/tu_0001", ev)
	}

	// No wait here: send msg2 the instant turn.completed was observed,
	// racing the reap in Run's select loop (see doc comment above).
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "two"}

	ev = recvWithin(t, out, testTimeout)
	if ev.Type != contracts.EvTurnStarted {
		t.Fatalf("event after msg2 = %+v; want turn.started (msg2 must NOT be silently dropped)", ev)
	}
	if ev.TurnID != "tu_0002" {
		t.Fatalf("turn 2 turn_id = %q; want tu_0002 (a fresh turn, not a reused/dropped one)", ev.TurnID)
	}
	recvWithin(t, out, testTimeout) // item.started
	recvWithin(t, out, testTimeout) // item.agent_message.delta
	recvWithin(t, out, testTimeout) // item.completed
	ev = recvWithin(t, out, testTimeout)
	if ev.Type != contracts.EvTurnCompleted || ev.TurnID != "tu_0002" {
		t.Fatalf("turn 2 completed = %+v; want turn.completed/tu_0002", ev)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}

// TestManager_MultiTurn_ReapRaceStress runs the same "second message right
// after completion" scenario as TestManager_MultiTurn_SecondMessageAfterCompletion
// many times in one process, to give Go's pseudo-random select a real
// chance to land on the losing order across many attempts rather than
// relying on one draw.
//
// History: an earlier revision of Run had runOneTurn emit its terminal
// event directly to out and separately `close()` a bare struct{} done
// channel afterward (via defer). That left TWO distinct races: (a) the
// FIX-2-class race this test is named for (turnDone-ready and in-ready
// simultaneously at select-scan time — Go's random tie-break could pick
// `in` first and see a stale turnCancel!=nil), and (b) a narrower one this
// same loop surfaced empirically under heavy iteration (~1-2 drops per
// 10000 attempts under -race): the gap between "the terminal event's send
// to out lands" and "the OTHER goroutine's deferred close(done) actually
// runs" — not a select-fairness bug, a genuine cross-goroutine scheduling
// gap no amount of reap-ordering logic can close. Both are eliminated
// architecturally, not statistically: runOneTurn now hands its terminal
// event to done itself (an unbuffered chan contracts.Event) instead of
// sending it to out directly, and Run is the one that forwards it to out
// — in the SAME select-case step that resets turnCancel/turnDone, so
// there is no window, of any size, for an InUserMessage to land in
// between "event visible" and "bookkeeping reset". Verified at up to
// 20000 iterations x 3 runs (60k attempts) under -race with zero drops;
// iterations is kept modest here purely for CI runtime, not because a
// residual reappears at higher counts.
func TestManager_MultiTurn_ReapRaceStress(t *testing.T) {
	const iterations = 1000
	for i := 0; i < iterations; i++ {
		provider := fake.NewProvider(fake.Step{Text: "first"}, fake.Step{Text: "second"})
		m := NewManager(fmt.Sprintf("th_stress_%d", i), provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_a", "tu_b"}}))

		in := make(chan contracts.Input, 2)
		out := make(chan contracts.Event, 16)
		runErr := make(chan error, 1)
		go func() { runErr <- m.Run(context.Background(), in, out) }()

		in <- contracts.Input{Type: contracts.InUserMessage, Text: "one"}
		if !drainToTurnCompleted(t, out, testTimeout) {
			t.Fatalf("iteration %d: turn 1 never completed", i)
		}

		// No wait: send msg2 the instant turn.completed was observed.
		in <- contracts.Input{Type: contracts.InUserMessage, Text: "two"}

		ev := recvWithin(t, out, testTimeout)
		if ev.Type != contracts.EvTurnStarted {
			t.Fatalf("iteration %d: event after msg2 = %+v; want turn.started (msg2 silently dropped)", i, ev)
		}
		if !drainToTurnCompleted(t, out, testTimeout) {
			t.Fatalf("iteration %d: turn 2 never completed", i)
		}

		in <- contracts.Input{Type: contracts.InEnd}
		expectClosed(t, out, testTimeout)
		if err := <-runErr; err != nil {
			t.Fatalf("iteration %d: Run returned %v; want nil", i, err)
		}
	}
}

// drainToTurnCompleted reads events from ch until it sees turn.completed
// (returning true) or the shared deadline elapses (returning false) —
// a single timer for the whole drain rather than one per recvWithin call,
// so the stress test's own overhead doesn't itself widen the race window
// it's trying to exercise.
func drainToTurnCompleted(t *testing.T, ch <-chan contracts.Event, d time.Duration) bool {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return false
			}
			if ev.Type == contracts.EvTurnCompleted {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// blockingProvider mimics a real subprocess-stream provider (like
// claudesdk — see provider/claudesdk/claudesdk.go's own `case <-ctx.Done():
// ... return partialResult(state, bridle.StopReasonAborted), nil`): it
// watches ctx itself and reports StopReasonAborted when the caller cancels
// mid-round, rather than erroring. This is the real contract shape a
// subprocess-stream provider uses to signal an interrupt, so scripting it
// here (rather than bridle/fake, which is synchronous and never blocks)
// exercises the same path Manager will hit against the real claude-sdk
// lane.
type blockingProvider struct {
	started chan struct{}
}

func newBlockingProvider() *blockingProvider {
	return &blockingProvider{started: make(chan struct{})}
}

func (p *blockingProvider) Name() bridle.ProviderID { return "test-blocking" }

func (p *blockingProvider) Capabilities() bridle.ProviderCapabilities {
	return bridle.ProviderCapabilities{Category: bridle.CategorySubprocessStream, SupportsCustomTools: true}
}

func (p *blockingProvider) RunTurn(ctx context.Context, _ bridle.ProviderRequest, _ bridle.EventSink) (bridle.ProviderResult, error) {
	close(p.started)
	<-ctx.Done()
	return bridle.ProviderResult{StopReason: bridle.StopReasonAborted}, nil
}

// TestManager_InterruptMidTurn: an interrupt Input arriving while a turn
// is in flight cancels that turn's context; the provider (see
// blockingProvider above) reports StopReasonAborted, and Manager maps
// that onto turn.failed{interrupted:true} — never turn.completed.
func TestManager_InterruptMidTurn(t *testing.T) {
	provider := newBlockingProvider()
	m := NewManager("th_test", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 8)

	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}

	started := recvWithin(t, out, testTimeout)
	if started.Type != contracts.EvTurnStarted {
		t.Fatalf("first event = %+v; want turn.started", started)
	}

	select {
	case <-provider.started:
	case <-time.After(testTimeout):
		t.Fatal("provider.RunTurn never started")
	}

	in <- contracts.Input{Type: contracts.InInterrupt}

	failed := recvWithin(t, out, testTimeout)
	if failed.Type != contracts.EvTurnFailed {
		t.Fatalf("second event = %+v; want turn.failed", failed)
	}
	var p turnFailedPayload
	if err := json.Unmarshal(failed.Payload, &p); err != nil {
		t.Fatalf("decode turn.failed payload: %v", err)
	}
	if !p.Interrupted {
		t.Fatalf("turn.failed payload = %+v; want interrupted:true", p)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}

// --- U-C2: the tool surface actually executes ---

// managerTestRoots builds a toolrunner.Roots over a fresh t.TempDir(), for
// WithRoots — never the process's real cwd, so these tests can never
// touch a file outside their own sandbox.
func managerTestRoots(t *testing.T) toolrunner.Roots {
	t.Helper()
	roots, err := toolrunner.NewRoots(t.TempDir())
	if err != nil {
		t.Fatalf("NewRoots: %v", err)
	}
	return roots
}

// lastToolResultMessage returns the tool_result ProviderMessage from req's
// Messages (the message run.go appends after executeToolCall runs), or
// fails the test if none is present.
func lastToolResultMessage(t *testing.T, req bridle.ProviderRequest) bridle.ProviderMessage {
	t.Helper()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "tool_result" {
			return req.Messages[i]
		}
	}
	t.Fatalf("no tool_result message in %+v", req.Messages)
	return bridle.ProviderMessage{}
}

// TestManager_ToolCall_ReadFileExecutesViaSurface drives a fake Step whose
// ToolCalls names read_file on a file this test seeded — the harness's
// executeToolCall dispatches it through Manager's surfaceRunner into the
// real toolrunner.Surface (fs family), NOT a stub: the file's actual disk
// content must come back in the next round's tool_result message, and the
// turn must complete normally.
func TestManager_ToolCall_ReadFileExecutesViaSurface(t *testing.T) {
	roots := managerTestRoots(t)
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, "hello.txt"), []byte("hello from disk"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolReadFile, Args: json.RawMessage(`{"path":"hello.txt"}`)},
		}},
		fake.Step{Text: "done"},
	)
	// read_file classifies as KindRead (NEX-782), which defaultPolicy()
	// auto-allows — no WithPolicy override needed; this test proves
	// DISPATCH (the fs family actually reads the file via the real
	// Surface), not approval semantics (which has its own dedicated
	// coverage in approval_test.go).
	m := NewManager("th_tool", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "read hello.txt"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	toolMsg := lastToolResultMessage(t, provider.LastRequest())
	if toolMsg.Content != `"hello from disk"` {
		t.Fatalf("tool_result content = %q; want the file's real content (JSON-encoded)", toolMsg.Content)
	}
}

// TestManager_TurnRequestTools_CarriesSurfaceSpecs asserts the model
// actually sees the fs/exec tool specs (TurnRequest.Tools -> lowered onto
// ProviderRequest.Tools) — not an empty list, which is what every turn
// carried before this unit.
func TestManager_TurnRequestTools_CarriesSurfaceSpecs(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_tools", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	surface := toolrunner.NewSurface(nil, toolrunner.NewFSFamily(roots), toolrunner.NewExecFamily(roots))
	wantSpecs, err := surface.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs: %v", err)
	}

	gotTools := provider.LastRequest().Tools
	if len(gotTools) != len(wantSpecs) {
		t.Fatalf("ProviderRequest.Tools len = %d; want %d (fs+exec specs)", len(gotTools), len(wantSpecs))
	}
	found := false
	for _, td := range gotTools {
		if td.Name == toolrunner.ToolReadFile {
			found = true
		}
	}
	if !found {
		t.Fatalf("ProviderRequest.Tools = %+v; want read_file present", gotTools)
	}
}

// TestManager_ToolCall_UnknownToolIsGoErrorNotSilentSuccess: the model
// calling a tool name no family/MCP handles must NOT look like a silent
// success (an empty tool_result with no error signal) — it must surface
// as bridle's "error: ..." tool_result text, driven by surfaceRunner
// returning a Go error for the dispatch failure (see surfacerunner.go).
// The turn still completes normally — a ToolRunner.Run error never aborts
// bridle's round loop (only Before/AfterToolCall hook errors do, and this
// runner never touches those).
func TestManager_ToolCall_UnknownToolIsGoErrorNotSilentSuccess(t *testing.T) {
	roots := managerTestRoots(t)

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: "does_not_exist", Args: json.RawMessage(`{}`)},
		}},
		fake.Step{Text: "done"},
	)
	// U-C3: allow-all — this test proves surfaceRunner's unknown-tool-name
	// Go-error mapping, not approval semantics.
	m := NewManager("th_tool", provider, WithRoots(roots), WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "call a bogus tool"}

	var sawFailed, sawCompleted bool
	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev := <-out:
			switch ev.Type {
			case contracts.EvTurnFailed:
				sawFailed = true
				break loop
			case contracts.EvTurnCompleted:
				sawCompleted = true
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for the turn to end")
		}
	}
	if sawFailed || !sawCompleted {
		t.Fatalf("turn ended abnormally (failed=%v completed=%v); want a normal turn.completed — a dispatch error must not abort the turn", sawFailed, sawCompleted)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	toolMsg := lastToolResultMessage(t, provider.LastRequest())
	if !strings.HasPrefix(toolMsg.Content, "error: ") || !strings.Contains(toolMsg.Content, "unknown tool") {
		t.Fatalf("tool_result content = %q; want an \"error: ...unknown tool...\" message, not a silent success", toolMsg.Content)
	}
}

// TestManager_ToolCall_ProtectedPathErrorDoesNotAbortTurn: a tool-level
// failure (Result.IsError — read_file on a protected .git path) must
// surface to the model as a normal tool_result ("error: ..."), NOT abort
// the turn (turn.failed) — matching bridle's own executeToolCall doc:
// "the model should see the error string and decide what to do."
func TestManager_ToolCall_ProtectedPathErrorDoesNotAbortTurn(t *testing.T) {
	roots := managerTestRoots(t)
	if err := os.MkdirAll(filepath.Join(roots.WorkingDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, ".git", "config"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed .git/config: %v", err)
	}

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolReadFile, Args: json.RawMessage(`{"path":".git/config"}`)},
		}},
		fake.Step{Text: "done"},
	)
	// read_file classifies as KindRead (NEX-782), which defaultPolicy()
	// auto-allows — no WithPolicy override needed; this test proves the fs
	// family's own protected-path enforcement (Result.IsError) still runs
	// unconditionally underneath the auto-allowed approval, not approval
	// semantics.
	m := NewManager("th_tool", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "read .git/config"}

	var sawFailed, sawCompleted bool
	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev := <-out:
			switch ev.Type {
			case contracts.EvTurnFailed:
				sawFailed = true
				break loop
			case contracts.EvTurnCompleted:
				sawCompleted = true
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for the turn to end")
		}
	}
	if sawFailed || !sawCompleted {
		t.Fatalf("turn ended abnormally (failed=%v completed=%v); want a normal turn.completed — a tool-level error must not abort the turn", sawFailed, sawCompleted)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	toolMsg := lastToolResultMessage(t, provider.LastRequest())
	if !strings.HasPrefix(toolMsg.Content, "error: ") || !strings.Contains(toolMsg.Content, "protected") {
		t.Fatalf("tool_result content = %q; want an \"error: ...protected...\" message", toolMsg.Content)
	}
}
