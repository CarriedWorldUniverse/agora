package tui

import (
	"context"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// TestLocalBackend_RoundTrip proves the in-process wiring end-to-end
// against a fake Engine (agoraio.ScriptedEngine) — no daemon, no socket,
// no turnengine/claudesdk in the loop (that real assembly is
// cmd/agora/inprocess.go's job; U-F1 is where the REAL claude-sdk turn
// gets a manual smoke test). Send reaches the Engine (which reacts by
// advancing its script); the resulting Events come back out on
// localBackend.Events().
func TestLocalBackend_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	engine := &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{
		{Events: []contracts.Event{
			{Type: contracts.EvTurnStarted},
			{Type: contracts.EvAgentMessageDelta, Payload: mustJSON(t, map[string]string{"delta": "hi"})},
			{Type: contracts.EvTurnCompleted},
		}},
	}}

	sess := agoraio.NewSession(ctx, "thread-local", engine)
	att := sess.Attach(agoraio.AttachInfo{
		ClientID:     "tui-local-test",
		Kind:         "tui",
		Capabilities: []contracts.Capability{contracts.CapInteractive, contracts.CapApprover},
	}, 0)

	backend := NewLocalBackend(sess, att)
	defer backend.Close()

	if err := backend.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var got []contracts.EventType
	for len(got) < 3 {
		select {
		case ev, ok := <-backend.Events():
			if !ok {
				t.Fatalf("Events() closed early, got %v", got)
			}
			got = append(got, ev.Type)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for events, got %v", got)
		}
	}
	want := []contracts.EventType{contracts.EvTurnStarted, contracts.EvAgentMessageDelta, contracts.EvTurnCompleted}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("event[%d] = %v, want %v (all: %v)", i, got[i], w, got)
		}
	}
}

// TestLocalBackend_CloseDetachesAndTearsDownSession proves Close does more
// than a bare Detach: after Close returns, the underlying Session's Engine
// goroutine has fully drained (Session.Close's documented contract), and a
// second Close is safe (sync.Once) — this is the "no goroutine leak" half
// of the brief's TDD ask. We can't directly observe the engine goroutine
// exiting from outside the io package, so this proves the two behaviors we
// CAN observe from here: Close is idempotent (no panic/double-close error
// on att/sess), and Events() is not read from again after Close (the
// backend contract doesn't require the channel to close — Attachment.Events
// documents it is "never closed by the Session" — only that Close itself
// completes and can be called more than once without hanging or panicking).
func TestLocalBackend_CloseDetachesAndTearsDownSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	engine := &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{
		{Events: []contracts.Event{{Type: contracts.EvTurnStarted}, {Type: contracts.EvTurnCompleted}}},
	}}
	sess := agoraio.NewSession(ctx, "thread-local-close", engine)
	att := sess.Attach(agoraio.AttachInfo{ClientID: "tui-local-close-test", Kind: "tui"}, 0)
	backend := NewLocalBackend(sess, att)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := backend.Close(); err != nil {
			t.Errorf("first Close: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s — Session.Close likely wedged")
	}

	// A second Close must not hang or panic (sync.Once guard).
	if err := backend.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
