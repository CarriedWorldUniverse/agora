package turnengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// slowProvider takes a real, observable amount of time to produce its
// answer — the property every existing pipe/manager test lacked. The
// fake.Provider returns instantly, so a turn always finished before a
// closed input channel could be processed, which is exactly why #118
// (EOF killing an in-flight turn) survived a full suite for so long: the
// bug is a RACE the fast path always wins.
type slowProvider struct {
	started chan struct{}
	delay   time.Duration
	text    string
}

func newSlowProvider(delay time.Duration, text string) *slowProvider {
	return &slowProvider{started: make(chan struct{}), delay: delay, text: text}
}

func (p *slowProvider) Name() bridle.ProviderID { return "test-slow" }

func (p *slowProvider) Capabilities() bridle.ProviderCapabilities {
	return bridle.ProviderCapabilities{Category: bridle.CategoryDirectAPI, SupportsCustomTools: true}
}

func (p *slowProvider) RunTurn(ctx context.Context, _ bridle.ProviderRequest, sink bridle.EventSink) (bridle.ProviderResult, error) {
	close(p.started)
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		// Cancelled before finishing — report it honestly so the Manager
		// maps it onto turn.failed{interrupted:true}, which is what the
		// pre-fix behavior produced and what these tests assert against.
		return bridle.ProviderResult{StopReason: bridle.StopReasonAborted}, nil
	}
	sink.Emit(bridle.ModelChunk{Text: p.text})
	return bridle.ProviderResult{FinalText: p.text, StopReason: bridle.StopReasonModelDone}, nil
}

// TestManager_InputEOF_LetsTheInFlightTurnFinish is #118's regression
// test: closing `in` means "no more input is coming", NOT "abandon the
// work you are doing". Before the fix this produced
// turn.failed{interrupted:true} with no agent output — which is what
// `echo "fix the test" | agora pipe` did on every real (non-fake) run.
func TestManager_InputEOF_LetsTheInFlightTurnFinish(t *testing.T) {
	provider := newSlowProvider(150*time.Millisecond, "SLOW_ANSWER")
	m := NewManager("th_eof", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 64)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	<-provider.started // the turn is genuinely in flight
	close(in)          // EOF, exactly as agora pipe does at end of stdin

	var sawCompleted, sawFailed bool
	var finalText string
	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				break loop
			}
			switch ev.Type {
			case contracts.EvTurnCompleted:
				sawCompleted = true
			case contracts.EvTurnFailed:
				sawFailed = true
			case contracts.EvItemCompleted:
				if ev.Item != nil && ev.Item.Type == contracts.ItemAgentMessage {
					var p struct {
						Text string `json:"text"`
					}
					_ = json.Unmarshal(ev.Payload, &p)
					finalText = p.Text
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for out to close")
		}
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	if sawFailed {
		t.Error("turn.failed after EOF — the in-flight turn was killed instead of allowed to finish (#118)")
	}
	if !sawCompleted {
		t.Fatal("no turn.completed — the turn did not run to completion after EOF")
	}
	if finalText != "SLOW_ANSWER" {
		t.Errorf("agent message = %q; want the provider's real answer, proving the turn actually produced output", finalText)
	}
}

// The counterpart contract: an EXPLICIT contracts.InEnd still stops the
// turn immediately. EOF and InEnd must NOT collapse back into the same
// behavior — that collapse was the bug.
func TestManager_ExplicitEnd_StillStopsTheInFlightTurn(t *testing.T) {
	provider := newSlowProvider(10*time.Second, "SHOULD_NOT_APPEAR")
	m := NewManager("th_end", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 2)
	out := make(chan contracts.Event, 64)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	<-provider.started
	start := time.Now()
	in <- contracts.Input{Type: contracts.InEnd}

	// Drain to close rather than expectClosed: a buffered turn.started is
	// still in flight, and this test cares about TIMING, not about out
	// being empty at the instant InEnd lands.
	deadline := time.After(testTimeout)
drain:
	for {
		select {
		case _, ok := <-out:
			if !ok {
				break drain
			}
		case <-deadline:
			t.Fatal("timed out waiting for out to close after InEnd")
		}
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Must not have waited out the provider's 10s delay.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("InEnd took %v; it must cancel the turn, not wait for it", elapsed)
	}
}

// A cancelled ctx during EOF-drain must still cut the turn short —
// otherwise a shutting-down caller (SIGINT, daemon stopping) would hang
// waiting out a slow turn it has explicitly abandoned.
func TestManager_InputEOF_CancelledContextStillCutsTheTurnShort(t *testing.T) {
	provider := newSlowProvider(10*time.Second, "SHOULD_NOT_APPEAR")
	m := NewManager("th_eof_ctx", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 64)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(ctx, in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	<-provider.started
	start := time.Now()
	cancel()  // caller is shutting down
	close(in) // and its input stream ends

	deadline := time.After(testTimeout)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				<-runErr
				if elapsed := time.Since(start); elapsed > 3*time.Second {
					t.Fatalf("EOF with a cancelled ctx took %v; must not wait out the turn", elapsed)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out; a cancelled ctx during EOF-drain did not cut the turn short")
		}
	}
}

// TestManager_InputEOF_ThenCancel_CutsTheDrainShort covers the ordering
// the sibling test above does not: EOF lands FIRST, so the drain is
// already blocked in finishInFlight, and only then does the caller cancel.
// The sibling cancels before closing in, so the Run loop's own ctx.Done()
// branch wins the select and the drain is never entered at all.
//
// The scenario is real — a piped run hits EOF mid-turn and the operator
// hits Ctrl-C. Note this passes with or without finishInFlight's own
// ctx.Done() case, because turnCtx derives from ctx: cancelling reaches
// the provider directly and turnDone then fires on its own. See the
// limitation noted on finishInFlight.
func TestManager_InputEOF_ThenCancel_CutsTheDrainShort(t *testing.T) {
	provider := newSlowProvider(10*time.Second, "SHOULD_NOT_APPEAR")
	m := NewManager("th_eof_then_cancel", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 64)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(ctx, in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	<-provider.started
	close(in) // EOF FIRST: the drain is now blocked waiting on the turn

	// Give the drain a moment to actually enter finishInFlight, so the
	// cancel below is observed there rather than by the Run loop.
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	cancel()

	deadline := time.After(testTimeout)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				<-runErr
				if elapsed := time.Since(start); elapsed > 3*time.Second {
					t.Fatalf("cancel during the EOF drain took %v; the ctx escape did not fire", elapsed)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out; cancelling during the EOF drain did not cut the turn short")
		}
	}
}

// EOF with NO turn in flight (the common case: a session that already
// finished, or never started one) must still wind down cleanly.
func TestManager_InputEOF_NoTurnInFlightIsClean(t *testing.T) {
	m := NewManager("th_eof_idle", newSlowProvider(time.Second, "x"))
	in := make(chan contracts.Input)
	out := make(chan contracts.Event, 8)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	close(in)
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
