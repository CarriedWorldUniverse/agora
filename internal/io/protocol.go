// Package io: the session protocol wire frames.
// Spec: agora-spec-io.md §2, §0a.
package io

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdio "io"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// maxClientFrameBytes bounds a single session-protocol client frame (attach
// or input line). json.NewDecoder(rw) with no size cap lets an oversized
// frame OOM the daemon (the ws NetConn also disables its own read limit);
// pipe mode already bounds lines the same way via bufio.Scanner
// (maxPipeLineBytes) — this mirrors that backstop for the session protocol.
const maxClientFrameBytes = 1 << 20 // 1 MiB

// AttachRequest is the first frame a session-protocol connection sends —
// which thread to join, who the client is, and how much backlog to replay.
// Spec: agora-spec-io.md §0a ("attach {thread_id, replay: N}").
type AttachRequest struct {
	ThreadID     string                 `json:"thread_id"`
	ClientID     string                 `json:"client_id"`
	Kind         string                 `json:"kind,omitempty"`
	Capabilities []contracts.Capability `json:"capabilities,omitempty"`
	Replay       int                    `json:"replay,omitempty"`
}

// ClientFrame is one line a session-protocol connection sends. Exactly one
// field is set: Attach (always the first frame on a connection) or Input
// (every frame after).
//
// Spec-ambiguity call: pipe mode explicitly carries "no method envelope"
// (§1) because it has one implicit session and one client. The session
// protocol multiplexes threads and declares per-client capabilities before
// any Input is meaningful, so SOME envelope is unavoidable here — this is
// the simplest one that still lets every frame after the first ride the
// exact same contracts.Input the pipe/library transports use, unmodified.
// Spec: agora-spec-io.md §2.
type ClientFrame struct {
	Attach *AttachRequest   `json:"attach,omitempty"`
	Input  *contracts.Input `json:"input,omitempty"`
}

// ServerFrame is one line a session-protocol connection receives: the same
// contracts.Event vocabulary pipe mode emits (§0a: "same event types").
type ServerFrame struct {
	Event contracts.Event `json:"event"`
}

// ErrExpectedAttach is returned when a connection's first frame isn't an
// attach request.
var ErrExpectedAttach = errors.New("io: session protocol: first frame must be attach")

// frameCodec reads/writes newline-delimited JSON frames over any
// io.ReadWriteCloser. A net.Conn (unix socket) and a websocket.NetConn
// (coder/websocket's ws-to-net.Conn adapter, wsconn.go) both satisfy that
// interface, so this one codec drives both transports — the concrete
// difference between unix and ws lives entirely in how the
// io.ReadWriteCloser is obtained (listenUnix/dialUnix vs acceptWS/dialWS),
// never in framing.
// Spec: agora-spec-io.md §2 ("unix socket / ws framing").
type frameCodec struct {
	rw stdio.ReadWriteCloser
	sc *bufio.Scanner
	mu sync.Mutex // guards writes (and Close, FIX 7): the fan-out pump and (if ever needed) a direct writer could race
}

func newFrameCodec(rw stdio.ReadWriteCloser) *frameCodec {
	sc := bufio.NewScanner(rw)
	sc.Buffer(make([]byte, 0, 64*1024), maxClientFrameBytes)
	return &frameCodec{rw: rw, sc: sc}
}

// readClient reads one newline-delimited frame. Clients write frames with
// json.NewEncoder, which newline-terminates every value, so a bufio.Scanner
// (line-bounded to maxClientFrameBytes) is wire-compatible while capping
// memory: an oversized frame returns bufio.ErrTooLong instead of growing
// json.Decoder's internal buffer without bound.
func (c *frameCodec) readClient() (ClientFrame, error) {
	var f ClientFrame
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return f, err
		}
		return f, stdio.EOF
	}
	err := json.Unmarshal(c.sc.Bytes(), &f)
	return f, err
}

func (c *frameCodec) writeServer(f ServerFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("io: marshal server frame: %w", err)
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.rw.Write(b)
	return err
}

// Close closes the underlying connection. It holds c.mu so it can't race a
// concurrent writeServer (FIX 7).
func (c *frameCodec) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rw.Close()
}

// SessionLookup resolves a thread_id to its Session. The daemon (U18,
// internal/daemon) supplies the real multi-thread registry; tests use a
// trivial map-backed implementation (see sessionMap in transport_test.go).
type SessionLookup interface {
	Session(threadID string) (*Session, error)
}

// ServeConn drives one session-protocol connection end-to-end (§2): it
// reads the attach frame, attaches to the resolved Session, then
// concurrently pumps Attachment.Events() to the wire and Input frames off
// the wire into Attachment.Send, until the connection or ctx closes.
//
// ServeConn does not run in a goroutine itself — callers (unix/ws accept
// loops) spawn one per accepted connection.
func ServeConn(ctx context.Context, rw stdio.ReadWriteCloser, sessions SessionLookup) error {
	codec := newFrameCodec(rw)
	defer codec.Close()

	first, err := codec.readClient()
	if err != nil {
		return fmt.Errorf("io: read attach frame: %w", err)
	}
	if first.Attach == nil {
		return ErrExpectedAttach
	}
	sess, err := sessions.Session(first.Attach.ThreadID)
	if err != nil {
		return fmt.Errorf("io: resolve session %s: %w", first.Attach.ThreadID, err)
	}
	a := sess.Attach(AttachInfo{
		ClientID:     first.Attach.ClientID,
		Kind:         first.Attach.Kind,
		Capabilities: first.Attach.Capabilities,
	}, first.Attach.Replay)
	defer a.Detach()

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	writeDone := make(chan error, 1)
	go func() {
		for {
			select {
			case ev, ok := <-a.Events():
				if !ok {
					writeDone <- nil
					return
				}
				if err := codec.writeServer(ServerFrame{Event: ev}); err != nil {
					writeDone <- err
					return
				}
			case <-cctx.Done():
				writeDone <- nil
				return
			}
		}
	}()

	var readLoopErr error
	for {
		frame, err := codec.readClient()
		if err != nil {
			if errors.Is(err, stdio.EOF) {
				readLoopErr = nil
			} else {
				readLoopErr = err
			}
			break
		}
		if frame.Input == nil {
			continue // ignore a stray/duplicate attach frame after the first
		}
		if err := a.Send(cctx, *frame.Input); err != nil && !errors.Is(err, ErrAlreadyResolved) && !errors.Is(err, context.Canceled) {
			readLoopErr = err
			break
		}
	}
	cancel()
	<-writeDone
	return readLoopErr
}
