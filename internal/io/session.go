// Package io: the daemon-side multi-attach hub.
// Spec: agora-spec-io.md §0a.
package io

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// slowConsumerTimeout bounds how long the broadcaster waits for a slow
// (non-deltas) event to be delivered to one client before force-detaching
// it, so a single stuck or malicious client can never wedge the single
// broadcaster goroutine forever (FIX 3b). A package var, not a const, so
// tests can lower it.
var slowConsumerTimeout = 30 * time.Second

// ErrAlreadyResolved is returned by Attachment.Send when an approval or
// question response arrives for an id another client already answered
// (first-answer-wins, §0a). It is not a connection-level error: the losing
// client's own event stream already carries the approval.resolved{by}
// broadcast, exactly like every other attached client's.
var ErrAlreadyResolved = errors.New("io: approval/question already resolved by another client")

// ErrUnauthorized is returned by Attachment.Send (via Session.handleInput)
// when the sending client's declared Capabilities don't satisfy
// contracts.RequiredForInput(in.Type) (agora-spec-remote.md §4). This is U2's
// job — checking a declared capability against the input type; it is NOT the
// U16 auth-deferral question of who may DECLARE a capability in the first
// place (that's identity/enrollment, handled elsewhere).
var ErrUnauthorized = errors.New("io: client capability does not permit this input")

// eventBufferSize bounds each Attachment's fan-out channel. Only
// EvAgentMessageDelta is droppable by design (§1); every other event is
// delivered by blocking the broadcaster until the slow client drains, per
// §4's backpressure rule ("a slow consumer slows event emission, never
// drops except deltas").
const eventBufferSize = 256

// maxResolvedEntries bounds Session.resolved (FIX 6): one entry per
// approval/question id, never pruned otherwise, would grow unbounded over a
// long-lived session. When adding an id would exceed the cap, the oldest
// entry is evicted. Re-resolving an evicted (very old) id is acceptable —
// first-answer-wins arbitration only matters for ids still in flight, and
// an id old enough to fall out of an 8192-entry window is not.
const maxResolvedEntries = 8192

// AttachInfo identifies one attached client, carried on client.attached/
// client.detached presence events and used as the `by` attribution on
// approval.resolved.
//
// DEFERRED (not this unit): ClientID is caller-supplied and unauthenticated
// here — nothing stops one client from claiming another's client_id, or two
// concurrent attachments from colliding on the same id. Verifying/assigning
// identity is U16's job (device enrollment); this package trusts whatever
// AttachInfo it's handed.
// Spec: agora-spec-io.md §0a.
type AttachInfo struct {
	ClientID     string
	Kind         string // "tui" | "vessel" | "web" | ... — informational only
	Capabilities []contracts.Capability
}

// presencePayload is the payload shape carried by client.attached/
// client.detached events.
// Spec: agora-spec-io.md §0a ("client.attached {client_id, kind, capabilities}").
type presencePayload struct {
	ClientID     string                 `json:"client_id"`
	Kind         string                 `json:"kind,omitempty"`
	Capabilities []contracts.Capability `json:"capabilities,omitempty"`
}

// approvalResolvedPayload is the payload shape carried by approval.resolved.
// Spec: agora-spec-io.md §0a ("approval.resolved {by}").
type approvalResolvedPayload struct {
	ID string `json:"id"`
	By string `json:"by"`
}

// Attachment is one client's live connection to a Session (agora-spec-io.md
// §0 terminology: "an attachment is a client's live connection to the
// daemon"). Events delivers fan-out (plus, at Attach time, a replay tail);
// Send forwards client Input into the session with first-answer-wins
// arbitration; Detach unregisters and emits client.detached.
type Attachment struct {
	info   AttachInfo
	events chan contracts.Event
	closed chan struct{}
	sess   *Session
	once   sync.Once
}

// Events returns the channel this attachment receives fan-out (and replay)
// events on. Never closed by the Session (an attachment may Detach and be
// discarded by its owner; there is no reader left to notice a close).
func (a *Attachment) Events() <-chan contracts.Event { return a.events }

// Info returns the AttachInfo this attachment registered with.
func (a *Attachment) Info() AttachInfo { return a.info }

// Send forwards in into the session. For approval_response/
// question_response it enforces first-answer-wins: the first Send for a
// given id reaches the Engine and broadcasts approval.resolved to every
// attached client; every later Send for the same id returns
// ErrAlreadyResolved without reaching the Engine.
func (a *Attachment) Send(ctx context.Context, in contracts.Input) error {
	return a.sess.handleInput(ctx, a, in)
}

// Detach unregisters the attachment and broadcasts client.detached.
// Idempotent — safe to call more than once (e.g. from both a deferred
// cleanup and an explicit detach).
func (a *Attachment) Detach() {
	a.once.Do(func() { a.sess.detach(a) })
}

// Session owns one thread's Engine and fans its output to every attached
// client (agora-spec-io.md §0a). It is the daemon-side multi-attach seam;
// pipe mode is the same Engine interface driven by RunPipe without a
// Session — a single implicit client needs no fan-out or arbitration (§1:
// "pipe mode is the session protocol flattened to one implicit session and
// one client").
type Session struct {
	threadID string

	mu            sync.Mutex
	clients       map[*Attachment]struct{}
	backlog       []contracts.Event
	resolved      map[string]string // approval/question id -> resolving client_id
	resolvedOrder []string          // insertion order of resolved's keys, for FIX 6 eviction

	in     chan contracts.Input
	cancel context.CancelFunc
	done   chan struct{} // closed once the engine's out channel drains and closes
}

// NewSession starts engine.Run over a context derived from ctx and returns
// a Session ready to accept Attach calls. Close cancels that context and
// waits for the engine to finish draining.
func NewSession(ctx context.Context, threadID string, engine Engine) *Session {
	cctx, cancel := context.WithCancel(ctx)
	s := &Session{
		threadID: threadID,
		clients:  make(map[*Attachment]struct{}),
		resolved: make(map[string]string),
		in:       make(chan contracts.Input, 64),
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	out := make(chan contracts.Event, eventBufferSize)
	go func() { _ = engine.Run(cctx, s.in, out) }()
	go s.broadcastLoop(out)
	return s
}

// ThreadID returns the thread this session owns.
func (s *Session) ThreadID() string { return s.threadID }

func (s *Session) broadcastLoop(out <-chan contracts.Event) {
	defer close(s.done)
	for ev := range out {
		s.broadcast(ev)
	}
}

// broadcast appends ev to the replay backlog and fans it out to every
// currently-attached client.
func (s *Session) broadcast(ev contracts.Event) {
	s.broadcastExcept(ev, nil)
}

// broadcastExcept is broadcast, but skips delivering to except (still
// appends to the backlog, so a later replay sees it). Used for
// client.attached so a joining client doesn't get told about its own
// arrival — the presence event is for everyone ELSE (§0a: "so the TUI can
// show 'vessel is listening'"), which is also what makes ordering
// predictable for a client that just attached: replay tail, then only
// events for other clients' activity, never its own join.
func (s *Session) broadcastExcept(ev contracts.Event, except *Attachment) {
	s.mu.Lock()
	s.backlog = append(s.backlog, ev)
	recipients := make([]*Attachment, 0, len(s.clients))
	for a := range s.clients {
		if a == except {
			continue
		}
		recipients = append(recipients, a)
	}
	s.mu.Unlock()
	var stuck []*Attachment
	for _, a := range recipients {
		if deliverEvent(a, ev) {
			stuck = append(stuck, a)
		}
	}
	// A client that never drains its channel within slowConsumerTimeout gets
	// force-detached AFTER the delivery loop (FIX 3b) — never from inside
	// deliverEvent itself, so the one broadcaster goroutine can move on to
	// every other recipient instead of being wedged on the stuck one.
	for _, a := range stuck {
		a.Detach()
	}
}

// deliverEvent sends ev to a's channel and reports whether delivery timed
// out (a stuck slow consumer, FIX 3b). A delta is droppable (§1) — it never
// times out, only delivers-or-drops. Every other event blocks until
// delivered, the attachment detaches concurrently (a.closed), or
// slowConsumerTimeout elapses.
func deliverEvent(a *Attachment, ev contracts.Event) (timedOut bool) {
	if ev.Type == contracts.EvAgentMessageDelta {
		select {
		case a.events <- ev:
		case <-a.closed:
		default:
		}
		return false
	}
	select {
	case a.events <- ev:
		return false
	case <-a.closed:
		return false
	case <-time.After(slowConsumerTimeout):
		return true
	}
}

// Attach registers a new client. If replayN > 0 the attachment is seeded
// with up to the last replayN backlog events (clamped to eventBufferSize,
// a.events' capacity — see FIX 3a below) before it starts receiving live
// fan-out (§0a: "reattach replays a tail of recent items"). The backlog
// CONTENT the tail carries is ordered correctly relative to the first live
// event (the snapshot and the client's registration into the fan-out set
// happen under the same lock, in that order — tail first, register second —
// so no live event can land between the snapshot and the registration). A
// client.attached presence event is then broadcast to every OTHER attached
// client (§0a: "so the TUI can show 'vessel is listening'" — the
// notification is for observers of the new arrival, not the arriving
// client itself).
func (s *Session) Attach(info AttachInfo, replayN int) *Attachment {
	a := &Attachment{
		info:   info,
		events: make(chan contracts.Event, eventBufferSize),
		closed: make(chan struct{}),
		sess:   s,
	}

	// FIX 3a: clamp replayN to eventBufferSize (a.events' capacity) and push
	// the tail into a.events BEFORE registering a into s.clients, all under
	// the single s.mu lock. Because the clamped tail length never exceeds
	// cap(a.events) and the buffer is empty at this point, the pushes below
	// never block — an unbounded replayN with no reader draining yet used to
	// deadlock the caller (and hold s.mu the whole time, wedging every other
	// Session operation) when replayN > 256.
	if replayN > eventBufferSize {
		replayN = eventBufferSize
	}

	s.mu.Lock()
	var tail []contracts.Event
	if replayN > 0 {
		start := len(s.backlog) - replayN
		if start < 0 {
			start = 0
		}
		tail = append(tail, s.backlog[start:]...)
	}
	for _, ev := range tail {
		a.events <- ev
	}
	s.clients[a] = struct{}{}
	s.mu.Unlock()

	s.broadcastExcept(contracts.Event{
		Type:     contracts.EvClientAttached,
		ThreadID: s.threadID,
		Payload:  mustMarshal(presencePayload(info)),
	}, a)
	return a
}

func (s *Session) detach(a *Attachment) {
	s.mu.Lock()
	delete(s.clients, a)
	s.mu.Unlock()
	close(a.closed)
	// a was just removed from s.clients, so broadcast already excludes it;
	// broadcastExcept(ev, a) here is equivalent but documents the intent.
	s.broadcastExcept(contracts.Event{
		Type:     contracts.EvClientDetached,
		ThreadID: s.threadID,
		Payload:  mustMarshal(presencePayload(a.info)),
	}, a)
}

// handleInput routes one client's Input into the session. approval_response/
// question_response get first-answer-wins arbitration (§0a); everything
// else passes straight through to the Engine.
func (s *Session) handleInput(ctx context.Context, from *Attachment, in contracts.Input) error {
	if !contracts.Holds(from.info.Capabilities, contracts.RequiredForInput(in.Type)) {
		return ErrUnauthorized
	}
	if (in.Type == contracts.InApprovalResponse || in.Type == contracts.InQuestionResponse) && in.ID != "" {
		s.mu.Lock()
		if _, already := s.resolved[in.ID]; already {
			s.mu.Unlock()
			return ErrAlreadyResolved
		}
		s.resolved[in.ID] = from.info.ClientID
		s.resolvedOrder = append(s.resolvedOrder, in.ID)
		if len(s.resolvedOrder) > maxResolvedEntries {
			oldest := s.resolvedOrder[0]
			s.resolvedOrder = s.resolvedOrder[1:]
			delete(s.resolved, oldest)
		}
		s.mu.Unlock()

		s.broadcast(contracts.Event{
			Type:     contracts.EvApprovalResolved,
			ThreadID: s.threadID,
			Payload:  mustMarshal(approvalResolvedPayload{ID: in.ID, By: from.info.ClientID}),
		})
	}
	select {
	case s.in <- in:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close cancels the engine's context and waits for its output to fully
// drain (so a caller can rely on every event the engine ever emits having
// reached the backlog/fan-out before Close returns).
//
// FIX 3c: before cancel()+<-s.done, Close snapshots s.clients and Detaches
// every one of them. Detach closes a.closed, which unblocks any
// deliverEvent parked in broadcastLoop (so it can drain the rest of out and
// broadcastLoop can return, closing s.done) and lets each client's own
// ServeConn reader/writer exit — without this, a non-draining client with
// events still pending would hang Close forever waiting on a full
// a.events channel that nothing is reading.
//
// DEFERRED (not this unit): s.in (the channel handleInput sends Input into)
// is never closed by Close — cancel() stops the Engine reading it via ctx,
// but a caller that races another handleInput against Close could still
// briefly send on s.in after cancel(). This is mitigated today by the
// parent ctx cancellation propagating through handleInput's own
// select{s.in <- in; <-ctx.Done()}; a stronger guarantee (e.g. closing s.in
// exactly once, race-free, from Close) is left for whichever later unit
// tightens Session's shutdown contract.
func (s *Session) Close() {
	s.mu.Lock()
	clients := make([]*Attachment, 0, len(s.clients))
	for a := range s.clients {
		clients = append(clients, a)
	}
	s.mu.Unlock()
	for _, a := range clients {
		a.Detach()
	}

	s.cancel()
	<-s.done
}
