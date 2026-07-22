package main

import (
	"context"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/daemon"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// TestNewEngineFactory_ServesARealTurn proves the NEW construction path
// (newEngineFactory, engine.go) wired into a real internal/daemon.Daemon
// actually runs a turn: thread create -> user_message -> turn.completed,
// with the fake bridle provider injected through the refactored helper's
// seam (newEngineFactory(provider, store) takes provider as a plain
// parameter — production passes claudesdk.New(), this test passes
// fake.NewProvider) instead of daemon.Config{} with no EngineFactory at
// all (the bug this unit fixes — internal/daemon/registry.go's
// ErrNoEngineFactory). Also asserts the turn's items landed in the SAME
// store the daemon was configured with (persistence.LocalStore, not
// MemStore, matching production).
func TestNewEngineFactory_ServesARealTurn(t *testing.T) {
	store, err := persistence.NewLocalStore(t.TempDir(), persistence.Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	provider := fake.NewProvider(fake.Step{
		Text:  "hello from the fake daemon turn",
		Usage: bridle.Usage{InputTokens: 3, OutputTokens: 2},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := daemon.NewDaemon(ctx, daemon.Config{
		Store:         store,
		EngineFactory: newEngineFactory(provider, store, subagent.NewMemGraphStore()),
	})
	defer d.Close()

	threadID, err := d.CreateThread(contracts.ThreadMeta{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	sess, err := d.Session(threadID)
	if err != nil {
		t.Fatalf("Session(%q): %v", threadID, err)
	}

	att := sess.Attach(agoraio.AttachInfo{
		ClientID:     "test-client",
		Kind:         "test",
		Capabilities: []contracts.Capability{contracts.CapInteractive},
	}, 0)
	defer att.Detach()

	if err := att.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "hi"}); err != nil {
		t.Fatalf("Send user_message: %v", err)
	}

	deadline := time.After(5 * time.Second)
	sawCompleted := false
	for !sawCompleted {
		select {
		case ev := <-att.Events():
			if ev.Type == contracts.EvTurnFailed {
				t.Fatalf("turn.failed: %+v", ev)
			}
			if ev.Type == contracts.EvTurnCompleted {
				sawCompleted = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn.completed")
		}
	}

	// The turn's items were persisted to the daemon's store, not just
	// fanned out over the wire.
	it, err := store.Resume(threadID)
	if err != nil {
		t.Fatalf("Resume(%q): %v", threadID, err)
	}
	defer it.Close()

	var sawAgentMessage bool
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type == contracts.TIAgentMessage {
			sawAgentMessage = true
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate resume: %v", err)
	}
	if !sawAgentMessage {
		t.Fatal("expected a persisted agent_message item in the store after the turn")
	}
}
