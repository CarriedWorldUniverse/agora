package tui

import (
	"context"
	"encoding/json"
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
