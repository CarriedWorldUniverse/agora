package io

import (
	"context"
	"encoding/json"
	"fmt"
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

	early := sess.Attach(AttachInfo{ClientID: "early", Capabilities: []contracts.Capability{contracts.CapInteractive}}, 0)
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

// TestSession_ReplayClampAvoidsDeadlock: Attach with a replayN far larger
// than eventBufferSize (a.events' capacity) returns promptly instead of
// deadlocking (FIX 3a — previously Attach registered the client THEN pushed
// an unbounded tail, which blocked forever with no reader draining when
// replayN > eventBufferSize), and the replayed tail itself never exceeds
// eventBufferSize events.
func TestSession_ReplayClampAvoidsDeadlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// One scripted turn emitting well over eventBufferSize events, so the
	// backlog has more than a.events could ever hold.
	const n = eventBufferSize + 50
	events := make([]contracts.Event, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, contracts.Event{Type: contracts.EvItemCompleted, ThreadID: "th_clamp"})
	}
	engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}
	sess := NewSession(ctx, "th_clamp", engine)
	defer sess.Close()

	seed := sess.Attach(AttachInfo{ClientID: "seed", Capabilities: []contracts.Capability{contracts.CapInteractive}}, 0)
	if err := seed.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "go"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for i := 0; i < n; i++ {
		recvWithin(t, seed.Events(), 5*time.Second)
	}
	seed.Detach()

	done := make(chan *Attachment, 1)
	go func() {
		done <- sess.Attach(AttachInfo{ClientID: "late"}, 100000)
	}()

	var late *Attachment
	select {
	case late = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Attach with replayN=100000 did not return promptly (deadlock)")
	}
	defer late.Detach()

	count := 0
	for {
		select {
		case <-late.Events():
			count++
		case <-time.After(300 * time.Millisecond):
			if count > eventBufferSize {
				t.Fatalf("replay tail delivered %d events, want <= %d (eventBufferSize)", count, eventBufferSize)
			}
			return
		}
	}
}

// TestSession_SlowConsumerForceDetached: a non-draining attachment gets
// force-detached after slowConsumerTimeout elapses (FIX 3b), and a second,
// draining attachment keeps receiving events regardless — the stuck client
// never wedges the single broadcaster goroutine.
func TestSession_SlowConsumerForceDetached(t *testing.T) {
	old := slowConsumerTimeout
	slowConsumerTimeout = 100 * time.Millisecond
	defer func() { slowConsumerTimeout = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine := &ScriptedEngine{}
	sess := NewSession(ctx, "th_slow", engine)
	defer sess.Close()

	stuck := sess.Attach(AttachInfo{ClientID: "stuck"}, 0)
	draining := sess.Attach(AttachInfo{ClientID: "draining"}, 0)
	defer draining.Detach()

	// Fill stuck's buffer completely without ever reading from it, so every
	// subsequent broadcast blocks on stuck. Run the sender to completion
	// (via broadcastDone) before the test returns and its deferred
	// sess.Close() runs, so nothing races broadcast internals.
	broadcastDone := make(chan struct{})
	go func() {
		defer close(broadcastDone)
		for i := 0; i < eventBufferSize+5; i++ {
			sess.broadcast(contracts.Event{Type: contracts.EvItemCompleted, ThreadID: "th_slow"})
		}
	}()

	// Keep draining continuously for the whole broadcast run (it must never
	// stall, even while stuck is wedged) until the sender finishes.
	drainingDone := make(chan struct{})
	go func() {
		defer close(drainingDone)
		for {
			select {
			case <-draining.Events():
			case <-broadcastDone:
				return
			}
		}
	}()

	select {
	case <-stuck.closed:
		// stuck was force-detached, as expected.
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: stuck was never force-detached")
	}

	select {
	case <-broadcastDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the broadcast sender goroutine to finish")
	}
	<-drainingDone
}

// TestSession_CloseDetachesAllAndReturns: Close() detaches every attached
// client before waiting on the engine to drain (FIX 3c), so it returns
// promptly even with a non-draining client attached and events still
// pending — without this, Close would hang forever on a full a.events
// channel with no reader.
func TestSession_CloseDetachesAllAndReturns(t *testing.T) {
	old := slowConsumerTimeout
	slowConsumerTimeout = 200 * time.Millisecond
	defer func() { slowConsumerTimeout = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A turn that emits more events than a stuck client's buffer can hold,
	// so its channel is full and never drained by the time Close runs.
	const n = eventBufferSize + 5
	events := make([]contracts.Event, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, contracts.Event{Type: contracts.EvItemCompleted, ThreadID: "th_close"})
	}
	engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}
	sess := NewSession(ctx, "th_close", engine)

	stuck := sess.Attach(AttachInfo{ClientID: "stuck", Capabilities: []contracts.Capability{contracts.CapInteractive}}, 0)
	if err := stuck.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "go"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Never read from stuck.Events() — it fills and stays full.
	time.Sleep(50 * time.Millisecond) // give the broadcaster time to fill stuck's buffer

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		sess.Close()
	}()

	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Session.Close hung with a non-draining client attached")
	}
}

// TestSession_ResolvedMapBounded: s.resolved (and its eviction-order
// tracking) never grows past maxResolvedEntries, no matter how many unique
// approval/question ids get resolved over a session's life (FIX 6).
func TestSession_ResolvedMapBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine := echoEngine{}
	sess := NewSession(ctx, "th_bounded", engine)
	defer sess.Close()

	client := sess.Attach(AttachInfo{ClientID: "c1", Capabilities: []contracts.Capability{contracts.CapApprover}}, 0)
	defer client.Detach()

	const total = maxResolvedEntries + 500
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("ap_%d", i)
		if err := client.Send(ctx, contracts.Input{Type: contracts.InApprovalResponse, ID: id, Decision: contracts.DecisionAllow}); err != nil {
			t.Fatalf("Send(%s): %v", id, err)
		}
		// Drain the approval.resolved + item.completed broadcasts this
		// resolution produces, so the client's own channel never fills and
		// blocks the broadcaster.
		recvWithin(t, client.Events(), 2*time.Second)
		recvWithin(t, client.Events(), 2*time.Second)
	}

	sess.mu.Lock()
	n := len(sess.resolved)
	nOrder := len(sess.resolvedOrder)
	sess.mu.Unlock()
	if n > maxResolvedEntries {
		t.Fatalf("len(s.resolved) = %d, want <= %d", n, maxResolvedEntries)
	}
	if nOrder > maxResolvedEntries {
		t.Fatalf("len(s.resolvedOrder) = %d, want <= %d", nOrder, maxResolvedEntries)
	}
}

// TestSession_CapabilityEnforcement: handleInput checks the sending
// client's declared Capabilities against contracts.RequiredForInput(in.Type)
// (FIX 1) — an attachment lacking the required capability gets
// ErrUnauthorized and the Input never reaches the Engine; one that holds it
// succeeds.
func TestSession_CapabilityEnforcement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine := echoEngine{}
	sess := NewSession(ctx, "th_cap", engine)
	defer sess.Close()

	cases := []struct {
		name string
		in   contracts.Input
	}{
		{"config needs admin", contracts.Input{Type: contracts.InConfig, Key: "k", Value: json.RawMessage(`1`)}},
		{"user_message needs interactive", contracts.Input{Type: contracts.InUserMessage, Text: "go"}},
		{"approval_response needs approver", contracts.Input{Type: contracts.InApprovalResponse, ID: "ap_cap", Decision: contracts.DecisionAllow}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Observer capability (or none) never satisfies any of these.
			unauthorized := sess.Attach(AttachInfo{ClientID: "observer-" + tc.name, Capabilities: []contracts.Capability{contracts.CapObserver}}, 0)
			defer unauthorized.Detach()
			if err := unauthorized.Send(ctx, tc.in); err != ErrUnauthorized {
				t.Fatalf("Send with insufficient capability: err = %v, want ErrUnauthorized", err)
			}
		})
	}

	// An attachment holding the required capability succeeds and reaches
	// the Engine (echoEngine echoes it back as item.completed).
	authorized := sess.Attach(AttachInfo{ClientID: "admin", Capabilities: []contracts.Capability{contracts.CapAdmin}}, 0)
	defer authorized.Detach()
	if err := authorized.Send(ctx, contracts.Input{Type: contracts.InConfig, Key: "k", Value: json.RawMessage(`1`)}); err != nil {
		t.Fatalf("Send with sufficient capability: unexpected err = %v", err)
	}
	// Drain authorized's own presence/other noise until the echoed
	// item.completed for the config input arrives.
	found := false
	for i := 0; i < 6; i++ {
		ev := recvWithin(t, authorized.Events(), 2*time.Second)
		if ev.Type == contracts.EvItemCompleted {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("authorized Send never reached the Engine (no item.completed observed)")
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
	c1 := sess.Attach(AttachInfo{ClientID: "c1", Capabilities: []contracts.Capability{contracts.CapApprover}}, 0)
	defer c1.Detach()
	c2 := sess.Attach(AttachInfo{ClientID: "c2", Capabilities: []contracts.Capability{contracts.CapApprover}}, 0)
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
