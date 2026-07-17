// approvals.go: the daemon-layer `by`-attribution side-channel (blueprint
// §6 resolution 1) and the approval-resolution glue engines use to turn a
// raw approval_response Input into the real contracts.ApprovalResolution.
package daemon

import (
	"context"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
)

// byLookup is the daemon-owned, io-untouched side-channel that lets an
// Engine learn WHO answered an approval_response/question_response Input —
// Input itself carries no By field (contracts/event.go: "a client cannot
// forge who answered"), and io.Session's arbitration (first-answer-wins)
// only broadcasts a THIN {id,by} event, it does not hand the winner's
// identity to the Engine directly. The daemon's connection-serving loop
// (serve.go) is the only writer: it calls Stash(id, clientID) immediately
// after a.Send(ctx, in) returns nil (io.ErrAlreadyResolved is what a
// losing racer gets back — that racer never stashes, so only the winner's
// clientID is ever recorded for a given id).
//
// WaitFor uses a per-id close-once channel rather than polling: a Resolve
// closure reading byOf(id) races the daemon's own connection goroutine
// (whose Stash call happens after the winning Send already placed the
// Input on the session's internal channel — see doc.go), so a plain map
// read is not guaranteed to observe the value yet. Blocking on a channel
// that Stash closes is race-free (synchronizes-before, not just
// mutex-protected) and needs no arbitrary retry/sleep.
type byLookup struct {
	mu   sync.Mutex
	vals map[string]string
	wait map[string]chan struct{}
}

func newByLookup() *byLookup {
	return &byLookup{vals: make(map[string]string), wait: make(map[string]chan struct{})}
}

func (b *byLookup) chanFor(id string) chan struct{} {
	if c, ok := b.wait[id]; ok {
		return c
	}
	c := make(chan struct{})
	b.wait[id] = c
	return c
}

// Stash records id -> clientID, first-write-wins (a second Stash for an id
// already recorded is a no-op — only the arbitration winner should ever
// legitimately call this, but staying idempotent costs nothing and guards
// against a caller mistake overwriting a correct value).
func (b *byLookup) Stash(id, clientID string) {
	if id == "" {
		return
	}
	b.mu.Lock()
	if _, exists := b.vals[id]; exists {
		b.mu.Unlock()
		return
	}
	b.vals[id] = clientID
	c := b.chanFor(id)
	b.mu.Unlock()
	close(c)
}

// WaitFor blocks until Stash(id, ...) has been called (returning the stashed
// clientID) or ctx is done. Safe to call before OR after the corresponding
// Stash.
func (b *byLookup) WaitFor(ctx context.Context, id string) (string, error) {
	b.mu.Lock()
	if v, ok := b.vals[id]; ok {
		b.mu.Unlock()
		return v, nil
	}
	c := b.chanFor(id)
	b.mu.Unlock()
	select {
	case <-c:
		b.mu.Lock()
		v := b.vals[id]
		b.mu.Unlock()
		return v, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// WaitForBy exposes the daemon's by-attribution side-channel to an Engine
// (or, in the conformance suite, a flowEngine's Resolve closure) — the
// EngineFactory-injected accessor blueprint §6 resolution 1 describes.
func (d *Daemon) WaitForBy(ctx context.Context, id string) (string, error) {
	return d.by.WaitFor(ctx, id)
}

// StashBy is exposed for pipe mode (and any caller not going through
// serve.go's connection loop) to record attribution explicitly — pipe mode
// has exactly one implicit client and no io.Session/Attachment at all
// (io/pipe.go), so there is no arbitration race to resolve; a caller that
// knows the attributing identity up front (e.g. a fixed test constant, per
// blueprint §3.2a) can stash it directly instead of going through a wire
// attach.
func (d *Daemon) StashBy(id, clientID string) { d.by.Stash(id, clientID) }

// ResolveApproval converts a raw approval_response Input plus its
// (daemon-attributed) actor into the real contracts.ApprovalResolution, by
// REUSING internal/approval's Result.Resolution conversion (blueprint §1.5)
// rather than hand-rolling the JSON shape a second time. scope defaults to
// contracts.ScopeOnce when the client didn't declare one (mirroring
// approval.Decide's own PolicyAuto default).
func ResolveApproval(id string, kind contracts.ApprovalKind, in contracts.Input, by string) contracts.ApprovalResolution {
	scope := in.Scope
	if scope == "" {
		scope = contracts.ScopeOnce
	}
	action := approval.ActionDeny
	if in.Decision == contracts.DecisionAllow {
		action = approval.ActionAllow
	}
	res := approval.Result{
		Action:  action,
		Kind:    kind,
		Scope:   scope,
		Stage:   contracts.StageApprover,
		By:      by,
		Message: in.Message,
	}
	resolution, _ := res.Resolution(id)
	return resolution
}
