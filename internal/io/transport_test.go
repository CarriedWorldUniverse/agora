package io

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

	c1 := dialAndAttach(t, sockPath, AttachRequest{ThreadID: "th_wire", ClientID: "c1", Kind: "tui"})
	defer c1.conn.Close()
	c2 := dialAndAttach(t, sockPath, AttachRequest{ThreadID: "th_wire", ClientID: "c2", Kind: "vessel"})
	defer c2.conn.Close()

	// c1 observes c2's attach (presence is for OTHER clients, per session.go).
	presence := c1.recvEvent(t, 3*time.Second)
	if presence.Type != contracts.EvClientAttached {
		t.Fatalf("c1 first event = %s, want client.attached", presence.Type)
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
