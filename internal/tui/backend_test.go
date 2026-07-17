package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// TestIOBackend_RoundTrip proves the real production wiring end-to-end
// against the actual U2 session-protocol machinery (io.Session +
// io.ServeUnix + io.ScriptedEngine) — no daemon binary exists yet (U18),
// but this is exactly the wire shape a daemon's ServeConn speaks.
func TestIOBackend_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sockPath := filepath.Join(t.TempDir(), "agora.sock")
	ln, err := agoraio.ListenUnix(sockPath)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()

	engine := &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{
		{Events: []contracts.Event{
			{Type: contracts.EvTurnStarted},
			{Type: contracts.EvAgentMessageDelta, Payload: mustJSON(t, map[string]string{"delta": "hi"})},
			{Type: contracts.EvTurnCompleted},
		}},
	}}
	sess := agoraio.NewSession(ctx, "thread-1", engine)
	defer sess.Close()

	sessions := singleSessionLookup{sess: sess}
	go agoraio.ServeUnix(ctx, ln, sessions)

	backend, err := DialUnixBackend(sockPath, agoraio.AttachRequest{
		ThreadID:     "thread-1",
		ClientID:     "tui-test",
		Kind:         "tui",
		Capabilities: []contracts.Capability{contracts.CapInteractive, contracts.CapApprover},
	})
	if err != nil {
		t.Fatalf("DialUnixBackend: %v", err)
	}
	defer backend.Close()

	if err := backend.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "hello"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var got []contracts.EventType
	for len(got) < 3 {
		select {
		case ev, ok := <-backend.Events():
			if !ok {
				t.Fatalf("Events() closed early, got %v", got)
			}
			got = append(got, ev.Type)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for events, got %v", got)
		}
	}
	want := []contracts.EventType{contracts.EvTurnStarted, contracts.EvAgentMessageDelta, contracts.EvTurnCompleted}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("event[%d] = %v, want %v (all: %v)", i, got[i], w, got)
		}
	}
}

// TestIOBackend_ReadLoopExitsOnCloseEvenWithFullEventsBuffer is finding #7
// (security/LOW): readLoop's `events <- f.Event` send was unconditional
// (one-case select) — with an unbuffered/full events channel and a wedged
// consumer, the goroutine leaked forever even after Close(). The events
// channel here is deliberately unbuffered and NOTHING ever reads from it,
// so the send can only ever complete via the new b.done escape.
func TestIOBackend_ReadLoopExitsOnCloseEvenWithFullEventsBuffer(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	b := &ioBackend{
		conn:   client,
		sc:     bufio.NewScanner(client),
		events: make(chan contracts.Event), // unbuffered: any send blocks
		done:   make(chan struct{}),
	}
	b.sc.Buffer(make([]byte, 0, 1024), maxServerFrameBytes)

	// exited is a dedicated signal, deliberately NOT b.events itself: if the
	// test read from b.events to detect the exit, that read would itself be
	// a receiver able to pair with readLoop's blocked send, racing against
	// the b.done case instead of proving it's the only viable path.
	exited := make(chan struct{})
	go func() {
		b.readLoop()
		close(exited)
	}()

	frame := agoraio.ServerFrame{Event: contracts.Event{Type: contracts.EvTurnStarted}}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	// net.Pipe's Write blocks until the matching Read completes — this
	// proves readLoop's Scan() actually consumed the frame before we ever
	// call Close(), so the goroutine is genuinely stuck trying to send to
	// the (unread) events channel, not just blocked in Scan().
	if _, err := server.Write(raw); err != nil {
		t.Fatalf("server.Write: %v", err)
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Nobody ever reads b.events, so the ONLY way readLoop's blocked send
	// resolves is via the b.done case.
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatalf("readLoop did not exit after Close() (goroutine leaked)")
	}
}

type singleSessionLookup struct{ sess *agoraio.Session }

func (s singleSessionLookup) Session(threadID string) (*agoraio.Session, error) {
	return s.sess, nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
