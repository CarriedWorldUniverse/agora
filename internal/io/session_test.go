package io

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// echoEngine is a minimal Engine for session tests: every Input it reads is
// echoed straight back out as an item.completed event carrying the input's
// Type/ID, so a test can assert on exactly what reached the Engine (and,
// for first-answer-wins, that only the WINNING input ever does).
type echoEngine struct{}

func (echoEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case i, ok := <-in:
			if !ok {
				return nil
			}
			payload, _ := json.Marshal(struct {
				Type contracts.InputType `json:"type"`
				ID   string              `json:"id"`
			}{Type: i.Type, ID: i.ID})
			select {
			case out <- contracts.Event{Type: contracts.EvItemCompleted, Payload: payload}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func recvWithin(t *testing.T, ch <-chan contracts.Event, d time.Duration) contracts.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(d):
		t.Fatal("timed out waiting for event")
		return contracts.Event{}
	}
}

func drainNone(t *testing.T, ch <-chan contracts.Event, d time.Duration) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("expected no event, got %+v", ev)
	case <-time.After(d):
	}
}

// TestSession_FanOutToAllAttached: a broadcast event from the Engine
// reaches every attached client.
func TestSession_FanOutToAllAttached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := []contracts.Event{
		{Type: contracts.EvThreadStarted, ThreadID: "th_1"},
		{Type: contracts.EvTurnCompleted, ThreadID: "th_1", Payload: json.RawMessage(`{"usage":{"input":1,"output":1}}`)},
	}
	engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}
	sess := NewSession(ctx, "th_1", engine)
	defer sess.Close()

	a1 := sess.Attach(AttachInfo{ClientID: "tui", Kind: "tui", Capabilities: []contracts.Capability{contracts.CapInteractive}}, 0)
	defer a1.Detach()
	a2 := sess.Attach(AttachInfo{ClientID: "vessel", Kind: "vessel", Capabilities: []contracts.Capability{contracts.CapInteractive}}, 0)
	defer a2.Detach()

	// a1's own attach; a2's attach (a1 sees it too); then the turn.
	if err := a1.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "go"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	for _, a := range []*Attachment{a1, a2} {
		var gotStarted, gotCompleted bool
		for i := 0; i < 6; i++ { // presence + the two turn events, generously bounded
			ev := recvWithin(t, a.Events(), 2*time.Second)
			if ev.Type == contracts.EvThreadStarted {
				gotStarted = true
			}
			if ev.Type == contracts.EvTurnCompleted {
				gotCompleted = true
				break
			}
		}
		if !gotStarted || !gotCompleted {
			t.Fatalf("client %s missing events: started=%v completed=%v", a.Info().ClientID, gotStarted, gotCompleted)
		}
	}
}

// TestSession_Presence: attach/detach broadcast client.attached/
// client.detached to other attached clients (§0a).
func TestSession_Presence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine := &ScriptedEngine{}
	sess := NewSession(ctx, "th_presence", engine)
	defer sess.Close()

	a1 := sess.Attach(AttachInfo{ClientID: "tui"}, 0)
	defer a1.Detach()

	a2 := sess.Attach(AttachInfo{ClientID: "vessel"}, 0)

	ev := recvWithin(t, a1.Events(), 2*time.Second)
	if ev.Type != contracts.EvClientAttached {
		t.Fatalf("a1 first event = %s, want client.attached", ev.Type)
	}
	var p presencePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("decode presence payload: %v", err)
	}
	if p.ClientID != "vessel" {
		t.Fatalf("presence client_id = %s, want vessel", p.ClientID)
	}

	a2.Detach()
	ev = recvWithin(t, a1.Events(), 2*time.Second)
	if ev.Type != contracts.EvClientDetached {
		t.Fatalf("a1 event after detach = %s, want client.detached", ev.Type)
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("decode presence payload: %v", err)
	}
	if p.ClientID != "vessel" {
		t.Fatalf("detach presence client_id = %s, want vessel", p.ClientID)
	}
}

// TestSession_Replay: a late-attaching client with replay:N gets the last N
// backlog events before any new live event (§0a: "reattach replays a tail
// of recent items").
func TestSession_Replay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := []contracts.Event{
		{Type: contracts.EvThreadStarted, ThreadID: "th_r"},
		{Type: contracts.EvTurnStarted, ThreadID: "th_r", TurnID: "tu_1"},
		{Type: contracts.EvItemCompleted, ThreadID: "th_r", TurnID: "tu_1", Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemAgentMessage}},
		{Type: contracts.EvTurnCompleted, ThreadID: "th_r", TurnID: "tu_1", Payload: json.RawMessage(`{"usage":{"input":1,"output":1}}`)},
	}
	engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}
	sess := NewSession(ctx, "th_r", engine)
	defer sess.Close()

	early := sess.Attach(AttachInfo{ClientID: "early"}, 0)
	defer early.Detach()
	if err := early.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "go"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Drain early's stream until the turn completes, so the backlog is
	// settled before the late attach snapshots it.
	for {
		ev := recvWithin(t, early.Events(), 2*time.Second)
		if ev.Type == contracts.EvTurnCompleted {
			break
		}
	}

	late := sess.Attach(AttachInfo{ClientID: "late"}, 2)
	defer late.Detach()

	first := recvWithin(t, late.Events(), 2*time.Second)
	second := recvWithin(t, late.Events(), 2*time.Second)
	if first.Type != contracts.EvItemCompleted || second.Type != contracts.EvTurnCompleted {
		t.Fatalf("replay tail = [%s, %s], want [item.completed, turn.completed]", first.Type, second.Type)
	}
	// late never sees its own client.attached presence (§0a presence is for
	// OTHER clients); nothing more should arrive on its stream.
	drainNone(t, late.Events(), 200*time.Millisecond)

	// "early" (the other attached client) is the one who observes late's
	// arrival.
	presence := recvWithin(t, early.Events(), 2*time.Second)
	if presence.Type != contracts.EvClientAttached {
		t.Fatalf("early's event after the turn = %s, want client.attached (late joining)", presence.Type)
	}
	var p presencePayload
	if err := json.Unmarshal(presence.Payload, &p); err != nil {
		t.Fatalf("decode presence payload: %v", err)
	}
	if p.ClientID != "late" {
		t.Fatalf("presence client_id = %s, want late", p.ClientID)
	}
}

// TestSession_FirstAnswerWins: two clients answer the same approval id
// concurrently; exactly one input reaches the Engine, and every attached
// client (including the loser) observes exactly one approval.resolved for
// that id, attributing the winner. Run with -race.
func TestSession_FirstAnswerWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine := echoEngine{}
	sess := NewSession(ctx, "th_race", engine)
	defer sess.Close()

	observer := sess.Attach(AttachInfo{ClientID: "observer"}, 0)
	defer observer.Detach()
	c1 := sess.Attach(AttachInfo{ClientID: "c1"}, 0)
	defer c1.Detach()
	c2 := sess.Attach(AttachInfo{ClientID: "c2"}, 0)
	defer c2.Detach()

	// Drain the two attach-presence events observer already received (c1's
	// and c2's — a joining client never sees its own presence event, so
	// observer's own attach produced none) before racing the answer, so
	// the resolved-count scan below only has to look for
	// approval.resolved / item.completed.
	drainPresence := func(n int) {
		for i := 0; i < n; i++ {
			recvWithin(t, observer.Events(), 2*time.Second)
		}
	}
	drainPresence(2)

	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = c1.Send(ctx, contracts.Input{Type: contracts.InApprovalResponse, ID: "ap_1", Decision: contracts.DecisionAllow})
	}()
	go func() {
		defer wg.Done()
		results[1] = c2.Send(ctx, contracts.Input{Type: contracts.InApprovalResponse, ID: "ap_1", Decision: contracts.DecisionDeny})
	}()
	wg.Wait()

	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else if err != ErrAlreadyResolved {
			t.Fatalf("unexpected Send error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1 (results=%v)", winners, results)
	}

	// The observer sees exactly one approval.resolved for ap_1, then
	// exactly one item.completed (echoEngine only ever saw the winner).
	resolvedCount := 0
	completedCount := 0
	for i := 0; i < 2; i++ {
		ev := recvWithin(t, observer.Events(), 2*time.Second)
		switch ev.Type {
		case contracts.EvApprovalResolved:
			resolvedCount++
			var p approvalResolvedPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode resolved payload: %v", err)
			}
			if p.ID != "ap_1" || (p.By != "c1" && p.By != "c2") {
				t.Fatalf("resolved payload = %+v, want id=ap_1 by=c1|c2", p)
			}
		case contracts.EvItemCompleted:
			completedCount++
		}
	}
	if resolvedCount != 1 || completedCount != 1 {
		t.Fatalf("resolvedCount=%d completedCount=%d, want 1 and 1 (engine must see the winner exactly once)", resolvedCount, completedCount)
	}
	drainNone(t, observer.Events(), 200*time.Millisecond)
}
