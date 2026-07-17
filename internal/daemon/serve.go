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
// nil), authenticate falls back to trusting the wire-declared kind/
// capabilities verbatim — an explicit, narrowly-scoped dev/test-only
// convenience (e.g. a daemon smoke test that isn't exercising capability
// enforcement at all) documented here so it is never mistaken for the
// production default; every conformance drive that exercises capability
// enforcement configures a real *remote.Registry.
func (d *Daemon) authenticate(req agoraio.AttachRequest) (agoraio.AttachInfo, error) {
	if d.registry == nil {
		return agoraio.AttachInfo{ClientID: req.ClientID, Kind: req.Kind, Capabilities: req.Capabilities}, nil
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

	info, err := d.authenticate(*first.Attach)
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
		go func() { _ = d.ServeConn(ctx, conn) }()
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
