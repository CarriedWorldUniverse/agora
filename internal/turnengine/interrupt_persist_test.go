package turnengine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// TestManager_InterruptedTurnPersists (NEX-798): an interrupted turn is NOT
// dropped from the thread — the user message (and any partial output) lands in
// the store, so /exit-mid-turn leaves the JSONL reflecting the exchange for
// resume. (Errored turns still don't persist.)
func TestManager_InterruptedTurnPersists(t *testing.T) {
	provider := newBlockingProvider()
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_intp", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager("th_intp", provider, WithStore(store), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 8)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "remember the pack size is 32MiB"}
	if ev := recvWithin(t, out, testTimeout); ev.Type != contracts.EvTurnStarted {
		t.Fatalf("first event = %+v; want turn.started", ev)
	}
	select {
	case <-provider.started:
	case <-time.After(testTimeout):
		t.Fatal("provider.RunTurn never started")
	}

	in <- contracts.Input{Type: contracts.InInterrupt}
	failed := recvWithin(t, out, testTimeout)
	if failed.Type != contracts.EvTurnFailed {
		t.Fatalf("event after interrupt = %+v; want turn.failed", failed)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	it, err := store.Resume("th_intp")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	var texts []string
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type == contracts.TIUserMessage {
			var p struct {
				Text string `json:"text"`
			}
			if err := decodePayload(item.Payload, &p); err != nil {
				t.Fatalf("decode: %v", err)
			}
			texts = append(texts, p.Text)
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(texts) != 1 || texts[0] != "remember the pack size is 32MiB" {
		t.Fatalf("persisted user messages = %v; want the interrupted turn's message", texts)
	}
	_ = bridle.StopReasonAborted // doc anchor: the persisted path is the aborted stop reason
}

// rudeBlockingProvider mimics what the REAL openai provider does on ctx
// cancellation: returns an ERROR with a ZERO StopReason — not a polite
// StopReasonAborted. This is the shape the live /exit path actually sees; the
// first version of this feature only tested the polite shape and the live
// persist never fired (the mock-the-producer's-real-output lesson).
type rudeBlockingProvider struct{ started chan struct{} }

func newRudeBlockingProvider() *rudeBlockingProvider {
	return &rudeBlockingProvider{started: make(chan struct{})}
}
func (p *rudeBlockingProvider) Name() bridle.ProviderID { return "rude" }
func (p *rudeBlockingProvider) Capabilities() bridle.ProviderCapabilities {
	return bridle.ProviderCapabilities{Category: bridle.CategoryDirectAPI, SupportsCustomTools: true, SupportsBeforeToolCall: true, SupportsAfterToolCall: true}
}
func (p *rudeBlockingProvider) RunTurn(ctx context.Context, _ bridle.ProviderRequest, _ bridle.EventSink) (bridle.ProviderResult, error) {
	close(p.started)
	<-ctx.Done()
	return bridle.ProviderResult{}, ctx.Err() // zero StopReason + error — the real shape
}

// TestManager_InterruptedTurnPersists_RealErrorShape: the NEX-798 regression
// for the live miss — ctx-cancellation surfacing as (zero StopReason, error)
// must still classify as interrupted and persist the exchange.
func TestManager_InterruptedTurnPersists_RealErrorShape(t *testing.T) {
	provider := newRudeBlockingProvider()
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_rude", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager("th_rude", provider, WithStore(store), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 8)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "interrupt me rudely"}
	if ev := recvWithin(t, out, testTimeout); ev.Type != contracts.EvTurnStarted {
		t.Fatalf("first event = %+v; want turn.started", ev)
	}
	select {
	case <-provider.started:
	case <-time.After(testTimeout):
		t.Fatal("provider.RunTurn never started")
	}

	in <- contracts.Input{Type: contracts.InInterrupt}
	// The provider error emits a non-terminal `error` event first — drain to
	// the terminal event.
	var failed contracts.Event
	for {
		ev := recvWithin(t, out, testTimeout)
		if ev.Type == contracts.EvTurnFailed || ev.Type == contracts.EvTurnCompleted {
			failed = ev
			break
		}
	}
	if failed.Type != contracts.EvTurnFailed {
		t.Fatalf("terminal after interrupt = %+v; want turn.failed", failed)
	}
	var p turnFailedPayload
	if err := json.Unmarshal(failed.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !p.Interrupted {
		t.Fatalf("turn.failed = %+v; want interrupted:true even for the error shape", p)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	it, err := store.Resume("th_rude")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	var found bool
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type == contracts.TIUserMessage {
			found = true
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if !found {
		t.Fatal("interrupted turn's user message not persisted (the live /exit miss)")
	}
}
