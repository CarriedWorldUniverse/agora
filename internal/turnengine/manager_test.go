package turnengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
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
