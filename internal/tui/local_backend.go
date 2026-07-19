package tui

import (
	"context"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// localBackend is the in-process Backend (agora-engine-blueprint.md U-E1,
// agora-spec-io.md §0a): no daemon, no socket — the turn engine runs as an
// io.Engine inside this same process, wrapped in an *agoraio.Session/
// Attachment exactly like a daemon-hosted thread would be. Send/Events are
// thin passthroughs to the Attachment (which already implements exactly
// this shape — see internal/io/session.go's Attachment.Send/Events); Close
// is the one place this type adds anything: it detaches this Attachment
// AND tears the Session (and therefore the underlying Engine's Run
// goroutine, and everything IT owns) down, which a bare Detach alone would
// not do. Mirrors ioBackend's shape (dial, wrap, Close tears down)
// one level up.
type localBackend struct {
	sess *agoraio.Session
	att  *agoraio.Attachment

	closeOnce sync.Once
}

var _ Backend = (*localBackend)(nil)

// NewLocalBackend wraps an already-Attach'd session/attachment pair into a
// Backend. Exported as its own step (mirroring DialUnixBackend/
// DialWSBackend one level up) so it is directly testable against an
// io.Session built over a fake/scripted Engine, with no cmd/agora,
// turnengine, or claudesdk in the loop — see local_backend_test.go. The
// production caller (cmd/agora/inprocess.go's newInProcessBackend) is the
// only other place this is constructed, over a real
// turnengine.Manager-backed Session.
func NewLocalBackend(sess *agoraio.Session, att *agoraio.Attachment) Backend {
	return &localBackend{sess: sess, att: att}
}

func (b *localBackend) Send(ctx context.Context, in contracts.Input) error {
	return b.att.Send(ctx, in)
}

func (b *localBackend) Events() <-chan contracts.Event { return b.att.Events() }

// Close detaches this Attachment, then closes the Session it came from.
// Session.Close cancels the Engine's Run context and blocks until Run's
// output has fully drained (internal/io/session.go's Close doc comment), so
// by the time Close returns here, the in-process Engine's goroutine (for
// turnengine.Manager: its harness/hooks, ctxmap engine, any per-turn state)
// is fully torn down, not merely detached from event delivery. Detaching
// first (rather than relying on Session.Close's own client sweep) mirrors
// what a well-behaved daemon-hosted client does on its own disconnect, and
// keeps this type's contract identical regardless of whether the Session
// happens to have other attachments (it never does, in the in-process
// launch path — this Backend owns its Session outright — but Session.Close
// tearing down every attached client either way makes this safe if that
// ever changes).
//
// sync.Once-guarded so a caller (main.go's defer plus an explicit shutdown
// path both calling Close) is safe, matching ioBackend.Close's convention.
// Session.Close itself also tolerates a second call (cancel is idempotent;
// receiving from an already-closed s.done channel returns immediately), so
// this guard is redundant-but-cheap belt-and-suspenders, not load-bearing.
func (b *localBackend) Close() error {
	b.closeOnce.Do(func() {
		b.att.Detach()
		b.sess.Close()
	})
	return nil
}
