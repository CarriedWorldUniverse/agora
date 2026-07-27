// serve.go: the session-protocol connection loop (unix socket + ws), and
// pipe-mode passthrough. See doc.go for why this package does not reuse
// internal/io's ServeConn for the former.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdio "io"
	"net"
	"net/http"

	"github.com/coder/websocket"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
)

// maxFrameBytes bounds a single client frame, mirroring
// internal/io/protocol.go's maxClientFrameBytes backstop (that constant is
// unexported, so this package carries its own copy of the same value/
// rationale rather than reaching into io internals).
const maxFrameBytes = 1 << 20

// authenticate resolves an AttachRequest to server-derived AttachInfo via
// the daemon's device registry — the capabilities that end up on the
// resulting io.Session.Attach call are always the AUTHENTICATED device's
// registry grant (internal/remote.AttachInfo), never req.Capabilities
// (doc.go's CRITICAL note). It also enforces the U16 CheckThread handoff
// (blueprint §4): a device whose AllowedThreads constraint excludes
// req.ThreadID is refused here, before any Session.Attach call.
//
// When the daemon has no registry configured at all (Config.Registry left
// nil), authenticate FAILS CLOSED by default: the connecting client is
// granted CapObserver only (it can attach and watch a thread's event
// stream, but Session.handleInput's own capability gate then refuses every
// input needing CapInteractive/CapApprover/CapAdmin — it can never approve,
// admin, or interact). Trusting the wire-declared kind/capabilities
// verbatim is available ONLY via the explicit Config.InsecureTrustWireCaps
// opt-in (NewDaemon logs a loud warning when it's set) — a narrowly-scoped
// dev/test-only convenience, never the shipped `agora daemon` default
// (cmd/agora/daemon.go's runDaemon leaves it false). Every conformance
// drive that exercises capability enforcement configures a real
// *remote.Registry instead of relying on either nil-Registry path.
func (d *Daemon) authenticate(req agoraio.AttachRequest, local bool) (agoraio.AttachInfo, error) {
	if d.registry == nil {
		if d.insecureTrustWireCaps {
			return agoraio.AttachInfo{ClientID: req.ClientID, Kind: req.Kind, Capabilities: req.Capabilities}, nil
		}
		if local {
			// LOCAL OWNER (agora#133). A connection that arrived on the
			// unix socket is already restricted to the uid that started
			// the daemon: io.ListenUnix chmods the socket to 0700 at
			// creation, so the KERNEL refuses any other user's connect().
			// That is a real enforced control, not a wire-declared claim,
			// and it is the same trust boundary the in-process lane
			// already runs at — `agora` with no daemon gives the operator
			// full interactive control of their own threads.
			//
			// Without this the shipped `agora daemon` was unusable by
			// anyone, including the operator: cmd/agora never configures a
			// Registry, so every client fell to CapObserver below and
			// Session.handleInput refused every user message. Worse, it
			// was silent — dialBackend PREFERS a listening daemon, so
			// starting one turned the TUI into a window that renders
			// normally and drops everything typed into it.
			//
			// CapAdmin is deliberately NOT granted: admin operations
			// (revoking devices, killing others' sessions) should stay
			// behind a real registry identity rather than "is the local
			// user", which is a weaker claim than it looks once anything
			// else runs as that uid.
			return agoraio.AttachInfo{
				ClientID: req.ClientID,
				Kind:     req.Kind,
				Capabilities: []contracts.Capability{
					contracts.CapObserver, contracts.CapInteractive, contracts.CapApprover,
				},
			}, nil
		}
		return agoraio.AttachInfo{ClientID: req.ClientID, Kind: req.Kind, Capabilities: []contracts.Capability{contracts.CapObserver}}, nil
	}
	device, ok := d.registry.Get(req.ClientID)
	if !ok || device.Revoked {
		return agoraio.AttachInfo{}, fmt.Errorf("%w: %s", ErrUnknownDevice, req.ClientID)
	}
	if err := remote.CheckThread(device, req.ThreadID); err != nil {
		return agoraio.AttachInfo{}, err
	}
	return remote.AttachInfo(device, req.Kind), nil
}

// ServeConn drives one session-protocol connection end-to-end, mirroring
// io.ServeConn's shape (read attach, resolve Session, pump Attachment
// events out / Input frames in) but substituting authenticate's
// server-derived AttachInfo for the wire-declared one, and stashing
// by-attribution (approvals.go's byLookup) for approval_response/
// question_response Input right after a successful forward.
func (d *Daemon) ServeConn(ctx context.Context, rw stdio.ReadWriteCloser) error {
	// Remote by default: a caller holding only an io.ReadWriteCloser has
	// not established that the peer is the local owner. ServeUnix, which
	// has, calls serveConn directly.
	return d.serveConn(ctx, rw, false)
}

func (d *Daemon) serveConn(ctx context.Context, rw stdio.ReadWriteCloser, local bool) error {
	defer rw.Close()
	sc := bufio.NewScanner(rw)
	sc.Buffer(make([]byte, 0, 64*1024), maxFrameBytes)

	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return fmt.Errorf("daemon: read attach frame: %w", err)
		}
		return stdio.EOF
	}
	var first agoraio.ClientFrame
	if err := json.Unmarshal(sc.Bytes(), &first); err != nil {
		return fmt.Errorf("daemon: decode attach frame: %w", err)
	}
	if first.Attach == nil {
		return ErrExpectedAttach
	}

	info, err := d.authenticate(*first.Attach, local)
	if err != nil {
		return err
	}
	sess, err := d.Session(first.Attach.ThreadID)
	if err != nil {
		return fmt.Errorf("daemon: resolve session %s: %w", first.Attach.ThreadID, err)
	}
	a := sess.Attach(info, first.Attach.Replay)
	defer a.Detach()

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// finding #4: the read loop below blocks in sc.Scan() with no read
	// deadline and no ctx.Done() escape of its own — on daemon ctx-cancel
	// (SIGTERM) with a connected-but-idle client, Scan() never returns and
	// this whole goroutine (plus the connection's FD, since rw.Close is
	// deferred and never fires) leaks. This watcher closes rw the instant
	// cctx is done, which makes the blocked Scan() return an error
	// immediately — rw.Close is safe to call more than once (the deferred
	// call above is a no-op after this one already ran).
	go func() {
		<-cctx.Done()
		_ = rw.Close()
	}()

	writeDone := make(chan error, 1)
	go func() {
		enc := json.NewEncoder(rw)
		for {
			select {
			case ev, ok := <-a.Events():
				if !ok {
					writeDone <- nil
					return
				}
				if err := enc.Encode(agoraio.ServerFrame{Event: ev}); err != nil {
					writeDone <- err
					return
				}
			case <-cctx.Done():
				writeDone <- nil
				return
			}
		}
	}()

	var readErr error
	for {
		if !sc.Scan() {
			readErr = sc.Err()
			break
		}
		var f agoraio.ClientFrame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			readErr = err
			break
		}
		if f.Input == nil {
			continue // stray/duplicate attach frame after the first, ignore
		}
		in := *f.Input
		// finding #2: narrow-scope enforcement (remote.CheckApproval's
		// AllowedApprovalKinds) on top of the coarse CapApprover tier
		// io.Session.handleInput already checks — refuse (skip forwarding,
		// keep the connection open for anything else this device IS
		// authorized for) rather than tearing down the whole connection,
		// since a device scoped to some approval kinds but not others is
		// not thereby misbehaving on every other input it might legally
		// send.
		if err := d.checkApprovalScope(cctx, info.ClientID, in); err != nil {
			continue
		}
		sendErr := a.Send(cctx, in)
		if sendErr == nil && (in.Type == contracts.InApprovalResponse || in.Type == contracts.InQuestionResponse) && in.ID != "" {
			// Only the arbitration WINNER's Send ever returns nil for a given
			// id (io.Session.handleInput: a loser gets ErrAlreadyResolved
			// before ever reaching the engine) — see approvals.go's byLookup
			// doc comment for why this is race-free despite happening after
			// forwarding, not strictly before it.
			d.by.Stash(in.ID, info.ClientID)
		}
		if sendErr != nil && !errors.Is(sendErr, agoraio.ErrAlreadyResolved) && !errors.Is(sendErr, context.Canceled) {
			readErr = sendErr
			break
		}
	}
	cancel()
	<-writeDone
	return readErr
}

// ServeUnix accepts connections on ln and drives ServeConn for each in its
// own goroutine, until ctx is done or Accept errors. Mirrors
// io.ServeUnix's shape (internal/io/transport_unix.go). Does not close ln —
// the caller owns the listener's lifecycle.
func (d *Daemon) ServeUnix(ctx context.Context, ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("daemon: accept: %w", err)
			}
		}
		// local=true: io.ListenUnix chmods the socket 0700, so the kernel
		// has already restricted this connection to the daemon's own uid.
		go func() { _ = d.serveConn(ctx, conn, true) }()
	}
}

// HandleWS upgrades r to a websocket and drives it through ServeConn —
// mirrors io.HandleWS's shape (internal/io/transport_ws.go), substituted
// with this package's authenticating ServeConn.
func (d *Daemon) HandleWS(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return fmt.Errorf("daemon: ws accept: %w", err)
	}
	rw := websocket.NetConn(ctx, conn, websocket.MessageText)
	err = d.ServeConn(ctx, rw)
	conn.Close(websocket.StatusNormalClosure, "")
	return err
}
