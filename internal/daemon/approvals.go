// approvals.go: the daemon-layer `by`-attribution side-channel (blueprint
// §6 resolution 1) and the approval-resolution glue engines use to turn a
// raw approval_response Input into the real contracts.ApprovalResolution.
package daemon

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
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

// approvalKindSnoop wraps a thread's real Engine so the daemon can learn an
// approval id's contracts.ApprovalKind BEFORE the approval.requested event
// it was raised on ever reaches a client (finding #2's fix: enforcing
// remote.CheckApproval(device, kind) on the approval-resolution path needs
// to know kind, and Input carries only the id, never the kind — see
// serve.go's read loop). Recording the id->kind mapping HERE, centrally,
// at the one place every thread's events already flow through on their way
// out (rather than snooping per-connection in ServeConn's write goroutine,
// which would race an attaching client's own response against that same
// connection's snoop of the request), guarantees the mapping is stashed
// before ANY client could possibly have seen (let alone answered) the
// request — no race to reason about.
type approvalKindSnoop struct {
	inner agoraio.Engine
	kinds *byLookup
}

func (s approvalKindSnoop) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	relay := make(chan contracts.Event, eventBufferSizeHint)
	errCh := make(chan error, 1)
	go func() { errCh <- s.inner.Run(ctx, in, relay) }()
	for ev := range relay {
		if ev.Type == contracts.EvApprovalRequested {
			var req contracts.ApprovalRequest
			if err := json.Unmarshal(ev.Payload, &req); err == nil && req.ID != "" {
				s.kinds.Stash(req.ID, string(req.Kind))
			}
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			// Drain relay so s.inner's Run (still writing to it) never
			// blocks forever on a send nobody will read after we bail.
			go func() {
				for range relay {
				}
			}()
			close(out)
			return <-errCh
		}
	}
	close(out)
	return <-errCh
}

// eventBufferSizeHint mirrors internal/io's own eventBufferSize (unexported
// there) — this relay channel only needs to be non-zero-buffered so the
// inner engine's Run never has to synchronize its send with this loop's
// receive+re-send 1:1; the exact size isn't load-bearing.
const eventBufferSizeHint = 256

// approvalKind resolves id to the contracts.ApprovalKind approvalKindSnoop
// recorded when the approval.requested event carrying it was raised.
// Blocks (bounded by ctx) if the mapping hasn't been stashed yet — safe
// because approvalKindSnoop guarantees the stash happens before the event
// (and therefore before any client's response) is ever delivered, so this
// only actually blocks for a malformed/forged id a client invents out of
// thin air, in which case ctx (the connection's own, canceled on teardown)
// is what bounds the wait.
func (d *Daemon) approvalKind(ctx context.Context, id string) (contracts.ApprovalKind, error) {
	kind, err := d.kinds.WaitFor(ctx, id)
	if err != nil {
		return "", err
	}
	return contracts.ApprovalKind(kind), nil
}

// checkApprovalScope enforces remote.CheckApproval(device, kind) on an
// InApprovalResponse before it is allowed to reach a.Send (finding #2): a
// CapApprover device whose registry-granted AllowedApprovalKinds constraint
// excludes the responded-to approval's kind is refused, even though the
// coarse CapApprover tier io.Session.handleInput already checked would
// otherwise let it through (RequiredForApproval collapses every non-question
// kind to the same CapApprover tier — the constraint is a NARROWING on top
// of that tier, and nothing before this call ever enforced it). Returns nil
// (permit) whenever there is no registry (capability enforcement is off
// entirely in that mode — serve.go's authenticate already governs what such
// a client can do) or the approval's kind can't be resolved in time (a
// forged/unknown id — the coarse capability/first-answer-wins checks
// downstream still apply; this is defense in depth, not the only gate).
func (d *Daemon) checkApprovalScope(ctx context.Context, clientID string, in contracts.Input) error {
	if d.registry == nil || in.Type != contracts.InApprovalResponse || in.ID == "" {
		return nil
	}
	device, ok := d.registry.Get(clientID)
	if !ok || device.Revoked {
		return ErrUnknownDevice
	}
	kind, err := d.approvalKind(ctx, in.ID)
	if err != nil {
		// Unknown id (never actually raised, or ctx died first) — nothing
		// further to narrow against; downstream checks (capability tier,
		// first-answer-wins) still apply.
		return nil
	}
	return remote.CheckApproval(device, kind)
}

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
