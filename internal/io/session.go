// Package io: the daemon-side multi-attach hub.
// Spec: agora-spec-io.md §0a.
package io

import (
	"context"
	"errors"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// ErrAlreadyResolved is returned by Attachment.Send when an approval or
// question response arrives for an id another client already answered
// (first-answer-wins, §0a). It is not a connection-level error: the losing
// client's own event stream already carries the approval.resolved{by}
// broadcast, exactly like every other attached client's.
var ErrAlreadyResolved = errors.New("io: approval/question already resolved by another client")

// eventBufferSize bounds each Attachment's fan-out channel. Only
// EvAgentMessageDelta is droppable by design (§1); every other event is
// delivered by blocking the broadcaster until the slow client drains, per
// §4's backpressure rule ("a slow consumer slows event emission, never
// drops except deltas").
const eventBufferSize = 256

// AttachInfo identifies one attached client, carried on client.attached/
// client.detached presence events and used as the `by` attribution on
// approval.resolved.
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

	mu       sync.Mutex
	clients  map[*Attachment]struct{}
	backlog  []contracts.Event
	resolved map[string]string // approval/question id -> resolving client_id

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
	for _, a := range recipients {
		deliverEvent(a, ev)
	}
}

// deliverEvent sends ev to a's channel. A delta is droppable (§1); every
// other event blocks until delivered or the attachment detaches
// concurrently (select on a.closed avoids hanging the broadcaster forever
// on a client that's going away mid-send).
func deliverEvent(a *Attachment, ev contracts.Event) {
	if ev.Type == contracts.EvAgentMessageDelta {
		select {
		case a.events <- ev:
		case <-a.closed:
		default:
		}
		return
	}
	select {
	case a.events <- ev:
	case <-a.closed:
	}
}

// Attach registers a new client. If replayN > 0 the attachment is seeded
// with up to the last replayN backlog events before it starts receiving
// live fan-out (§0a: "reattach replays a tail of recent items"), with no
// gap between the replayed tail and the first live event — the snapshot and
// the client's registration into the fan-out set happen under the same
// lock. A client.attached presence event is then broadcast to every OTHER
// attached client (§0a: "so the TUI can show 'vessel is listening'" — the
// notification is for observers of the new arrival, not the arriving
// client itself).
func (s *Session) Attach(info AttachInfo, replayN int) *Attachment {
	a := &Attachment{
		info:   info,
		events: make(chan contracts.Event, eventBufferSize),
		closed: make(chan struct{}),
		sess:   s,
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
	s.clients[a] = struct{}{}
	s.mu.Unlock()

	for _, ev := range tail {
		a.events <- ev
	}

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
	if (in.Type == contracts.InApprovalResponse || in.Type == contracts.InQuestionResponse) && in.ID != "" {
		s.mu.Lock()
		if _, already := s.resolved[in.ID]; already {
			s.mu.Unlock()
			return ErrAlreadyResolved
		}
		s.resolved[in.ID] = from.info.ClientID
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
func (s *Session) Close() {
	s.cancel()
	<-s.done
}
