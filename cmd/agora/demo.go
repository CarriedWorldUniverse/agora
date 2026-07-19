package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
)

// newDemoBackend builds an in-process backend whose "engine" is a scripted,
// zero-cost demoEngine — NO model, NO sidecar, NO subscription billing. It
// exists so the TUI render path (turn.started → running status → streamed
// reply → turn.completed) can be reproduced and debugged in a real terminal
// for free. Wired to `agora -demo`.
func newDemoBackend(ctx context.Context, attach agoraio.AttachRequest) (tui.Backend, error) {
	sess := agoraio.NewSession(ctx, attach.ThreadID, demoEngine{})
	att := sess.Attach(agoraio.AttachInfo{
		ClientID:     attach.ClientID,
		Kind:         attach.Kind,
		Capabilities: attach.Capabilities,
	}, attach.Replay)
	return tui.NewLocalBackend(sess, att), nil
}

// demoEngine plays one canned turn per user message with realistic timing so
// the "running" indicator is actually visible (and captured by `script`),
// then streams a short reply. It never calls a model.
type demoEngine struct{}

var _ agoraio.Engine = demoEngine{}

func (demoEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	turnN := 0
	emit := func(ev contracts.Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if msg.Type != contracts.InUserMessage {
				continue
			}
			turnN++
			turnID := "demo-turn"
			if !emit(contracts.Event{Type: contracts.EvTurnStarted, TurnID: turnID}) {
				return ctx.Err()
			}
			// Hold in the running state so the operator can SEE whether the
			// status row overwrites the prompt line.
			if !wait(1800 * time.Millisecond) {
				return ctx.Err()
			}
			for _, chunk := range []string{
				"This is a demo turn — no model was called, nothing is billed.\n",
				"Line two, streamed as a second chunk.\n",
				"If the prompt line survived the 'running' status, rendering is correct.",
			} {
				if !emit(contracts.Event{
					Type:    contracts.EvAgentMessageDelta,
					Payload: demoMarshal(map[string]string{"text": chunk}),
				}) {
					return ctx.Err()
				}
				if !wait(250 * time.Millisecond) {
					return ctx.Err()
				}
			}
			if !emit(contracts.Event{Type: contracts.EvTurnCompleted, TurnID: turnID}) {
				return ctx.Err()
			}
		}
	}
}

func demoMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
