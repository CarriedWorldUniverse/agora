package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
	"github.com/CarriedWorldUniverse/nexus/nexus/frames"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/coder/websocket"
)

func TestEscalationRoundTripFromBrokerPush(t *testing.T) {
	srv := newEscalationBroker(t)
	defer srv.Close()

	c, err := opclient.Dial(context.Background(), opclient.Config{
		BrokerURL: srv.URL,
		StateDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	req, err := frames.NewRequest(frames.KindEscalationRequest, frames.EscalationRequestPayload{
		Aspect: "maren",
		Tool:   "Bash",
		Args:   json.RawMessage(`{"command":"touch /tmp/ok"}`),
		Reason: "operator approval required",
	})
	if err != nil {
		t.Fatalf("request frame: %v", err)
	}
	srv.sendFrame(t, req)

	ev := expectEscalationEvent(t, c.Events())
	m := NewModel(Config{Agent: "maren", OperatorName: "casey", Client: c})
	updated, _ := m.Update(OpEventReceived{Event: ev})
	m = updated.(Model)

	if m.escalation == nil {
		t.Fatalf("modal not surfaced from escalation event")
	}
	if got := m.escalation.req.RequestID; got != req.ID {
		t.Fatalf("modal request id = %q, want %q", got, req.ID)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("approve produced no command")
	}
	if _, ok := cmd().(EscalationResolved); !ok {
		t.Fatalf("approve command did not return EscalationResolved")
	}

	dec := srv.expectFrame(t, string(frames.KindEscalationDecision))
	if dec.InReplyTo != "" {
		t.Fatalf("decision envelope in_reply_to = %q, want empty", dec.InReplyTo)
	}
	var payload frames.EscalationDecisionPayload
	if err := frames.PayloadAs(dec, &payload); err != nil {
		t.Fatalf("decision payload: %v", err)
	}
	if payload.Aspect != "maren" {
		t.Fatalf("decision aspect = %q, want maren", payload.Aspect)
	}
	if payload.Decision != frames.EscalationApprove {
		t.Fatalf("decision = %q, want approve", payload.Decision)
	}
	if payload.RequestID != req.ID {
		t.Fatalf("decision request_id = %q, want %q", payload.RequestID, req.ID)
	}
	if payload.Operator != "casey" {
		t.Fatalf("decision operator = %q, want casey", payload.Operator)
	}
}

type escalationBroker struct {
	URL string

	server *httptest.Server

	mu   sync.Mutex
	conn *websocket.Conn
	recv chan frames.Envelope
}

func newEscalationBroker(t *testing.T) *escalationBroker {
	t.Helper()
	f := &escalationBroker{recv: make(chan frames.Envelope, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/mode", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"bypass": true})
	})
	mux.HandleFunc("/connect", f.handleConnect)
	f.server = httptest.NewServer(mux)
	f.URL = f.server.URL
	return f
}

func (f *escalationBroker) Close() { f.server.Close() }

func (f *escalationBroker) handleConnect(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	for {
		typ, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		env, err := frames.Decode(data)
		if err != nil {
			continue
		}
		f.recv <- env
	}
}

func (f *escalationBroker) sendFrame(t *testing.T, env frames.Envelope) {
	t.Helper()
	raw, err := frames.Encode(env)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		t.Fatal("no websocket connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func (f *escalationBroker) expectFrame(t *testing.T, kind string) frames.Envelope {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case env := <-f.recv:
			if string(env.Kind) != kind {
				t.Fatalf("got frame kind %q, want %q", env.Kind, kind)
			}
			return env
		case <-timeout:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

func expectEscalationEvent(t *testing.T, ch <-chan opclient.Event) opclient.EscalationEvent {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if got, ok := ev.(opclient.EscalationEvent); ok {
				return got
			}
		case <-timeout:
			t.Fatalf("timed out waiting for escalation event")
		}
	}
}
