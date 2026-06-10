package opclient_test

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
	"github.com/coder/websocket"
)

func TestDialBypass(t *testing.T) {
	srv := newFakeBroker(t)
	defer srv.Close()

	c, err := opclient.Dial(context.Background(), opclient.Config{
		BrokerURL: srv.URL,
		StateDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if got := srv.lastConnectToken(); got != "" {
		t.Fatalf("bypass dial sent token %q, want empty", got)
	}
}

func TestRPCRoundTrip(t *testing.T) {
	srv := newFakeBroker(t)
	defer srv.Close()
	c := dialTestClient(t, srv)
	defer c.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := srv.expectFrame(t, "chat.list")
		var payload struct {
			AfterID int64 `json:"after_id"`
			Limit   int   `json:"limit"`
		}
		if err := frames.PayloadAs(req, &payload); err != nil {
			t.Errorf("request payload: %v", err)
		}
		if payload.AfterID != 0 || payload.Limit != 50 {
			t.Errorf("chat.list payload = %+v, want after_id=0 limit=50", payload)
		}
		srv.sendFrame(t, frames.Envelope{
			Kind:      "chat.list.result",
			InReplyTo: req.ID,
			TS:        time.Now().UTC(),
			Payload: mustJSON(t, map[string]any{
				"messages": []map[string]any{{
					"id": 7, "from": "maren", "content": "hi", "topic": "dm:maren",
				}},
				"has_more": false,
			}),
		})
	}()

	msgs, hasMore, err := c.ChatList(context.Background(), 0, 50)
	if err != nil {
		t.Fatalf("ChatList: %v", err)
	}
	if hasMore || len(msgs) != 1 || msgs[0].From != "maren" || msgs[0].Topic != "dm:maren" {
		t.Fatalf("ChatList = msgs=%+v hasMore=%v, want maren dm message", msgs, hasMore)
	}
	<-done
}

func TestChatSendNoResult(t *testing.T) {
	srv := newFakeBroker(t)
	defer srv.Close()
	c := dialTestClient(t, srv)
	defer c.Close()

	if err := c.ChatSend(context.Background(), "@maren hi", "dm:maren", 0); err != nil {
		t.Fatalf("ChatSend: %v", err)
	}
	req := srv.expectFrame(t, "chat.send")
	var payload struct {
		From    string `json:"from"`
		Content string `json:"content"`
		Topic   string `json:"topic"`
	}
	if err := frames.PayloadAs(req, &payload); err != nil {
		t.Fatalf("request payload: %v", err)
	}
	if payload.Content != "@maren hi" || payload.Topic != "dm:maren" {
		t.Fatalf("chat.send payload = %+v", payload)
	}
	// The broker's HandleChatSend rejects from=="" — regression for the
	// missing-from bug caught in the C2 live acceptance.
	if payload.From != "operator" {
		t.Fatalf("chat.send from = %q, want operator", payload.From)
	}
}

func TestSubscribePushDeliversMsgEvent(t *testing.T) {
	srv := newFakeBroker(t)
	defer srv.Close()
	c := dialTestClient(t, srv)
	defer c.Close()

	if err := c.Subscribe(context.Background(), "subscribe.chat"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = srv.expectFrame(t, "subscribe.chat")
	srv.sendFrame(t, frames.Envelope{
		Kind: "chat.update",
		ID:   "push-1",
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, map[string]any{
			"id": 11, "from": "maren", "content": "hello", "topic": "dm:maren",
		}),
	})
	ev := expectEvent[opclient.MsgEvent](t, c.Events())
	if ev.Message.ID != 11 || ev.Message.Topic != "dm:maren" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestReconnectCatchupFromCursor(t *testing.T) {
	srv := newFakeBroker(t)
	defer srv.Close()
	c := dialTestClient(t, srv)
	defer c.Close()

	if err := c.Subscribe(context.Background(), "subscribe.chat"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = srv.expectFrame(t, "subscribe.chat")
	srv.sendFrame(t, frames.Envelope{
		Kind: "chat.update",
		ID:   "push-1",
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, map[string]any{
			"id": 1, "from": "maren", "content": "first", "topic": "dm:maren",
		}),
	})
	_ = expectEvent[opclient.MsgEvent](t, c.Events())

	srv.dropConn()

	_ = srv.expectFrame(t, "subscribe.chat")
	req := srv.expectFrame(t, "chat.list")
	var payload struct {
		AfterID int64 `json:"after_id"`
	}
	if err := frames.PayloadAs(req, &payload); err != nil {
		t.Fatalf("chat.list payload: %v", err)
	}
	if payload.AfterID != 1 {
		t.Fatalf("catch-up after_id = %d, want 1", payload.AfterID)
	}
	srv.sendFrame(t, frames.Envelope{
		Kind:      "chat.list.result",
		InReplyTo: req.ID,
		TS:        time.Now().UTC(),
		Payload: mustJSON(t, map[string]any{
			"messages": []map[string]any{{
				"id": 2, "from": "maren", "content": "missed", "topic": "dm:maren",
			}},
			"has_more": false,
		}),
	})
	ev := expectEvent[opclient.MsgEvent](t, c.Events())
	if ev.Message.ID != 2 || ev.Message.Content != "missed" {
		t.Fatalf("catch-up event = %+v", ev)
	}
}

func dialTestClient(t *testing.T, srv *fakeBroker) *opclient.Client {
	t.Helper()
	c, err := opclient.Dial(context.Background(), opclient.Config{
		BrokerURL:    srv.URL,
		StateDir:     t.TempDir(),
		ReconnectMin: 10 * time.Millisecond,
		ReconnectMax: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return c
}

type fakeBroker struct {
	URL string

	t      *testing.T
	server *httptest.Server

	mu        sync.Mutex
	conn      *websocket.Conn
	lastToken string
	recv      chan frames.Envelope
}

func newFakeBroker(t *testing.T) *fakeBroker {
	t.Helper()
	f := &fakeBroker{t: t, recv: make(chan frames.Envelope, 64)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/mode", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"bypass": true})
	})
	mux.HandleFunc("/connect", f.handleConnect)
	f.server = httptest.NewServer(mux)
	f.URL = f.server.URL
	return f
}

func (f *fakeBroker) Close() { f.server.Close() }

func (f *fakeBroker) handleConnect(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.lastToken = r.URL.Query().Get("token")
	f.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	f.mu.Lock()
	if f.conn != nil {
		_ = f.conn.Close(websocket.StatusNormalClosure, "replaced")
	}
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

func (f *fakeBroker) lastConnectToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastToken
}

func (f *fakeBroker) expectFrame(t *testing.T, kind string) frames.Envelope {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case env := <-f.recv:
			if string(env.Kind) == kind {
				return env
			}
			t.Fatalf("got frame kind %q, want %q", env.Kind, kind)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

func (f *fakeBroker) sendFrame(t *testing.T, env frames.Envelope) {
	t.Helper()
	raw, err := frames.Encode(env)
	if err != nil {
		t.Fatalf("encode: %v", err)
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

func (f *fakeBroker) dropConn() {
	f.mu.Lock()
	conn := f.conn
	f.conn = nil
	f.mu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusAbnormalClosure, "drop")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func expectEvent[T any](t *testing.T, ch <-chan opclient.Event) T {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if got, ok := ev.(T); ok {
				return got
			}
		case <-timeout:
			var zero T
			t.Fatalf("timed out waiting for event %T", zero)
		}
	}
}
