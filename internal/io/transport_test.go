package io

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// sessionMap is the trivial SessionLookup a real daemon (U18) generalizes
// into a full thread registry; tests only need "one thread_id -> one
// Session".
type sessionMap struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func (m *sessionMap) Session(threadID string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[threadID]
	if !ok {
		return nil, fmt.Errorf("no such session: %s", threadID)
	}
	return s, nil
}

// dialClient is a tiny session-protocol test client: it dials, sends the
// attach frame, and gives the caller a channel of decoded server events
// plus a way to send Input frames.
type dialClient struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

func dialAndAttach(t *testing.T, sockPath string, req AttachRequest) *dialClient {
	t.Helper()
	conn, err := DialUnix(sockPath)
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	c := &dialClient{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
	if err := c.enc.Encode(ClientFrame{Attach: &req}); err != nil {
		t.Fatalf("send attach: %v", err)
	}
	return c
}

func (c *dialClient) sendInput(t *testing.T, in contracts.Input) {
	t.Helper()
	if err := c.enc.Encode(ClientFrame{Input: &in}); err != nil {
		t.Fatalf("send input: %v", err)
	}
}

// waitForAttachCount polls sess's registered client count until it reaches
// n or d elapses. dialAndAttach only guarantees the attach FRAME has been
// written to the wire, not that the server's accept-loop goroutine has
// actually run Session.Attach yet — two dialAndAttach calls back to back
// race each other's server-side registration order with no synchronization
// otherwise. Tests that need a deterministic "client X is attached before
// client Y dials" ordering poll this (rather than assuming dial order
// implies attach order, which the flaky prior version of this test did).
func waitForAttachCount(t *testing.T, sess *Session, n int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		sess.mu.Lock()
		count := len(sess.clients)
		sess.mu.Unlock()
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d attached client(s), have %d", n, count)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (c *dialClient) recvEvent(t *testing.T, d time.Duration) contracts.Event {
	t.Helper()
	type result struct {
		f   ServerFrame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var f ServerFrame
		err := c.dec.Decode(&f)
		ch <- result{f, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("decode server frame: %v", r.err)
		}
		return r.f.Event
	case <-time.After(d):
		t.Fatal("timed out waiting for server frame")
		return contracts.Event{}
	}
}

// TestServeUnix_MultiAttachOverRealSocket drives the full session protocol
// over an actual unix-domain socket (§2): two clients attach to the same
// thread, fan-out reaches both, and a third late-attaching client replays
// backlog. This is the same Session mechanics session_test.go exercises
// in-process, proved here over the wire framing too.
func TestServeUnix_MultiAttachOverRealSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "agora.sock")
	ln, err := ListenUnix(sockPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported on this Windows runtime: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := []contracts.Event{
		{Type: contracts.EvThreadStarted, ThreadID: "th_wire"},
		{Type: contracts.EvTurnCompleted, ThreadID: "th_wire", Payload: json.RawMessage(`{"usage":{"input":1,"output":1}}`)},
	}
	engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}
	sess := NewSession(ctx, "th_wire", engine)
	defer sess.Close()

	sessions := &sessionMap{sessions: map[string]*Session{"th_wire": sess}}

	go func() { _ = ServeUnix(ctx, ln, sessions) }()

	c1 := dialAndAttach(t, sockPath, AttachRequest{ThreadID: "th_wire", ClientID: "c1", Kind: "tui", Capabilities: []contracts.Capability{contracts.CapInteractive}})
	defer c1.conn.Close()
	// dialAndAttach only guarantees the attach frame reached the wire, not
	// that the server's accept-loop goroutine has run Session.Attach yet —
	// wait for that deterministically before dialing c2, so which of the
	// two clients' presence event the other observes isn't a race (a prior
	// version of this test assumed dial order implies attach order, which
	// was flaky).
	waitForAttachCount(t, sess, 1, 3*time.Second)
	c2 := dialAndAttach(t, sockPath, AttachRequest{ThreadID: "th_wire", ClientID: "c2", Kind: "vessel"})
	defer c2.conn.Close()

	// c1 observes c2's attach (presence is for OTHER clients, per session.go).
	presence := c1.recvEvent(t, 3*time.Second)
	if presence.Type != contracts.EvClientAttached {
		t.Fatalf("c1 first event = %s, want client.attached", presence.Type)
	}
	var p presencePayload
	if err := json.Unmarshal(presence.Payload, &p); err != nil {
		t.Fatalf("decode presence payload: %v", err)
	}
	if p.ClientID != "c2" {
		t.Fatalf("presence client_id = %s, want c2 (the joining client)", p.ClientID)
	}

	c1.sendInput(t, contracts.Input{Type: contracts.InUserMessage, Text: "go"})

	for _, c := range []*dialClient{c1, c2} {
		var sawCompleted bool
		for i := 0; i < 4; i++ {
			ev := c.recvEvent(t, 3*time.Second)
			if ev.Type == contracts.EvTurnCompleted {
				sawCompleted = true
				break
			}
		}
		if !sawCompleted {
			t.Fatalf("client did not observe turn.completed over the wire")
		}
	}

	// A late attach replays the backlog tail.
	c3 := dialAndAttach(t, sockPath, AttachRequest{ThreadID: "th_wire", ClientID: "c3", Kind: "web", Replay: 1})
	defer c3.conn.Close()
	tail := c3.recvEvent(t, 3*time.Second)
	if tail.Type != contracts.EvTurnCompleted {
		t.Fatalf("c3 replay tail[0] = %s, want turn.completed (the last backlog event)", tail.Type)
	}
}

// TestListenUnix_SocketPermissionHardened: ListenUnix chmods the socket to
// 0700 so it isn't world-connectable under a looser default umask (FIX 5).
// Unix-only — os.FileMode's owner/group/other bits aren't meaningful on
// Windows, and this package's own doc comments already note Windows AF_UNIX
// support is best-effort.
func TestListenUnix_SocketPermissionHardened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket permission bits aren't meaningful on Windows")
	}
	sockPath := filepath.Join(t.TempDir(), "agora-perm.sock")
	ln, err := ListenUnix(sockPath)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()

	fi, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Fatalf("socket mode = %o, want 0700", got)
	}
}

// TestServeConn_OversizedFrameRejected: a client frame larger than
// maxClientFrameBytes makes readClient/ServeConn return an error instead of
// growing memory without bound (FIX 2).
func TestServeConn_OversizedFrameRejected(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "agora3.sock")
	ln, err := ListenUnix(sockPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported on this Windows runtime: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()

	sessions := &sessionMap{sessions: map[string]*Session{}}
	serveErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		serveErr <- ServeConn(context.Background(), conn, sessions)
	}()

	conn, err := DialUnix(sockPath)
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	defer conn.Close()

	// A single line far larger than maxClientFrameBytes, newline-terminated
	// (as json.NewEncoder would produce), but never a valid JSON frame — the
	// scanner must reject it on size before json.Unmarshal ever sees it.
	oversized := make([]byte, maxClientFrameBytes+1024)
	for i := range oversized {
		oversized[i] = ' '
	}
	oversized = append(oversized, '\n')
	if _, err := conn.Write(oversized); err != nil {
		t.Fatalf("write oversized frame: %v", err)
	}

	select {
	case err := <-serveErr:
		if err == nil {
			t.Fatal("ServeConn returned nil error for an oversized frame, want an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ServeConn to reject the oversized frame")
	}
}

// TestFrameCodec_NormalFrameRoundTrips: a frame well under
// maxClientFrameBytes still round-trips through the bounded scanner (FIX 2
// regression guard: the size cap must not break ordinary frames).
func TestFrameCodec_NormalFrameRoundTrips(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		codec := newFrameCodec(server)
		f, err := codec.readClient()
		if err != nil {
			t.Errorf("readClient: %v", err)
			return
		}
		if f.Attach == nil || f.Attach.ThreadID != "th_normal" {
			t.Errorf("readClient frame = %+v, want attach.thread_id=th_normal", f)
		}
	}()

	enc := json.NewEncoder(client)
	if err := enc.Encode(ClientFrame{Attach: &AttachRequest{ThreadID: "th_normal", ClientID: "c1"}}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for normal frame round trip")
	}
}

// TestServeConn_FirstFrameMustBeAttach: a connection that sends an Input
// before ever attaching is rejected (§2 framing invariant).
func TestServeConn_FirstFrameMustBeAttach(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "agora2.sock")
	ln, err := ListenUnix(sockPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported on this Windows runtime: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()

	sessions := &sessionMap{sessions: map[string]*Session{}}
	serveErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		serveErr <- ServeConn(context.Background(), conn, sessions)
	}()

	conn, err := DialUnix(sockPath)
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	defer conn.Close()
	badInput := contracts.Input{Type: contracts.InUserMessage, Text: "oops"}
	if err := json.NewEncoder(conn).Encode(ClientFrame{Input: &badInput}); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case err := <-serveErr:
		if !errors.Is(err, ErrExpectedAttach) {
			t.Fatalf("ServeConn err = %v, want ErrExpectedAttach", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ServeConn to reject the connection")
	}
}
