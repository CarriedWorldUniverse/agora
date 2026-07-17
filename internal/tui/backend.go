package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	stdio "io"
	"net"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// Backend is what the tui Model talks to: send Input, receive Events. It is
// the seam that lets model.go (and its tests) be driven by either a real
// session-protocol connection or a fake/scripted stand-in.
type Backend interface {
	Send(ctx context.Context, in contracts.Input) error
	Events() <-chan contracts.Event
	Close() error
}

// maxServerFrameBytes bounds one server->client line, mirroring
// maxClientFrameBytes in internal/io/protocol.go (this package can't reuse
// that unexported constant/codec, but the same 1 MiB backstop applies here
// on the read side).
const maxServerFrameBytes = 1 << 20

// ioBackend is the real Backend: a session-protocol client (agora-spec-io.md
// §2) speaking newline-delimited JSON ClientFrame/ServerFrame over any
// net.Conn — a unix socket (local `agora daemon`, the common case) or a
// websocket-wrapped net.Conn (io.DialWS). internal/daemon (U18) is not yet
// built, so nothing hosts this seam in this repo today; the client side is
// still real, production wiring — it is exactly what a running daemon's
// ServeConn (internal/io/protocol.go) speaks, proven end-to-end against a
// real io.Session/io.ServeConn in backend_test.go.
type ioBackend struct {
	conn    net.Conn
	sc      *bufio.Scanner
	writeMu sync.Mutex

	events chan contracts.Event

	closeOnce sync.Once
	closeErr  error
	readErr   chan error
}

// DialUnixBackend dials a unix-socket session-protocol listener at path and
// attaches to threadID.
func DialUnixBackend(path string, attach agoraio.AttachRequest) (Backend, error) {
	conn, err := agoraio.DialUnix(path)
	if err != nil {
		return nil, err
	}
	return newIOBackend(conn, attach)
}

// DialWSBackend dials a websocket session-protocol endpoint and attaches to
// threadID.
func DialWSBackend(ctx context.Context, url string, attach agoraio.AttachRequest) (Backend, error) {
	conn, err := agoraio.DialWS(ctx, url)
	if err != nil {
		return nil, err
	}
	return newIOBackend(conn, attach)
}

// newIOBackend wraps an already-dialed connection: sends the attach frame,
// starts the read pump, and returns a ready Backend. Exported (indirectly,
// via Dial*Backend) as its own step mainly so tests can drive it over an
// in-process net.Pipe without a real listener.
func newIOBackend(conn net.Conn, attach agoraio.AttachRequest) (Backend, error) {
	b := &ioBackend{
		conn:    conn,
		sc:      bufio.NewScanner(conn),
		events:  make(chan contracts.Event, 256),
		readErr: make(chan error, 1),
	}
	b.sc.Buffer(make([]byte, 0, 64*1024), maxServerFrameBytes)

	if err := b.writeFrame(agoraio.ClientFrame{Attach: &attach}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tui: send attach frame: %w", err)
	}
	go b.readLoop()
	return b, nil
}

func (b *ioBackend) writeFrame(f agoraio.ClientFrame) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, err = b.conn.Write(raw)
	return err
}

func (b *ioBackend) readLoop() {
	defer close(b.events)
	for b.sc.Scan() {
		var f agoraio.ServerFrame
		if err := json.Unmarshal(b.sc.Bytes(), &f); err != nil {
			continue // malformed frame: drop, keep the connection alive
		}
		select {
		case b.events <- f.Event:
		}
	}
	err := b.sc.Err()
	if err == nil {
		err = stdio.EOF
	}
	b.readErr <- err
}

func (b *ioBackend) Send(ctx context.Context, in contracts.Input) error {
	done := make(chan error, 1)
	go func() { done <- b.writeFrame(agoraio.ClientFrame{Input: &in}) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *ioBackend) Events() <-chan contracts.Event { return b.events }

func (b *ioBackend) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.conn.Close()
	})
	return b.closeErr
}
