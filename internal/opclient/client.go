// Package opclient is the operator-side websocket client for agora.
package opclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/nexus/nexus/frames"
	"github.com/coder/websocket"
)

const (
	defaultRPCDeadline = 10 * time.Second
	defaultReconnect   = time.Second
	maxReconnect       = 32 * time.Second
)

// Config controls the operator websocket connection.
type Config struct {
	BrokerURL     string
	Token         string
	PinnedCertPEM string
	StateDir      string

	RPCDeadline  time.Duration
	ReconnectMin time.Duration
	ReconnectMax time.Duration
}

// Client is a long-lived operator websocket connection.
type Client struct {
	cfg       Config
	ctx       context.Context
	cancel    context.CancelFunc
	http      *http.Client
	tlsConfig *tls.Config

	connMu    sync.RWMutex
	conn      *websocket.Conn
	readDone  chan error
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]rpcPending

	subMu         sync.Mutex
	subscriptions map[string]json.RawMessage

	events chan Event

	cursorMu   sync.Mutex
	lastSeenID int64
	cursorBase int64
	seen       map[int64]struct{}
	cursorFile string
}

type rpcResult struct {
	env frames.Envelope
	err error
}

type rpcPending struct {
	wantKind frames.Kind
	ch       chan rpcResult
}

// ChatMessage is the operator chat message shape used by chat.list and chat.update.
type ChatMessage struct {
	ID         int64  `json:"id"`
	From       string `json:"from"`
	Content    string `json:"content"`
	ReplyTo    int64  `json:"reply_to,omitempty"`
	Topic      string `json:"topic,omitempty"`
	Thread     string `json:"thread,omitempty"`
	ReceivedAt string `json:"received_at,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Replay     bool   `json:"replay,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
	ThreadRoot int64  `json:"thread_root,omitempty"`
}

type ChatListResult struct {
	Messages []ChatMessage `json:"messages"`
	HasMore  bool          `json:"has_more"`
}

type RosterAspect = frames.RosterAspect

type Run struct {
	ID     string          `json:"id,omitempty"`
	Aspect string          `json:"aspect,omitempty"`
	Status string          `json:"status,omitempty"`
	Raw    json.RawMessage `json:"-"`
}

// Event is one asynchronous operator-client event.
type Event interface{ event() }

type MsgEvent struct{ Message ChatMessage }
type RunEvent struct{ Run Run }
type EscalationEvent struct {
	RequestID string
	Aspect    string          `json:"aspect"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}
type ConnState struct {
	Connected bool
	Err       error
}

func (MsgEvent) event()        {}
func (RunEvent) event()        {}
func (EscalationEvent) event() {}
func (ConnState) event()       {}

// Dial probes auth mode, opens the websocket, and starts the reader/reconnect loop.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.BrokerURL == "" {
		return nil, errors.New("opclient: BrokerURL required")
	}
	if cfg.RPCDeadline <= 0 {
		cfg.RPCDeadline = defaultRPCDeadline
	}
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = defaultReconnect
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = maxReconnect
	}
	tlsCfg, err := tlsConfigFromPinnedCert(cfg.PinnedCertPEM)
	if err != nil {
		return nil, err
	}
	httpClient := httpClientWithTLS(tlsCfg)

	stateDir := cfg.StateDir
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("opclient: resolve home: %w", err)
		}
		stateDir = filepath.Join(home, ".agora")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("opclient: mkdir state dir: %w", err)
	}

	cctx, cancel := context.WithCancel(ctx)
	c := &Client{
		cfg:           cfg,
		ctx:           cctx,
		cancel:        cancel,
		http:          httpClient,
		tlsConfig:     tlsCfg,
		readDone:      make(chan error, 1),
		pending:       make(map[string]rpcPending),
		subscriptions: make(map[string]json.RawMessage),
		events:        make(chan Event, 64),
		seen:          make(map[int64]struct{}),
		cursorFile:    filepath.Join(stateDir, "cursor.json"),
	}
	c.lastSeenID = c.loadCursor()
	c.cursorBase = c.lastSeenID
	if c.lastSeenID > 0 {
		c.seen[c.lastSeenID] = struct{}{}
	}
	if err := c.authProbe(ctx); err != nil {
		cancel()
		return nil, err
	}
	if err := c.connect(ctx); err != nil {
		cancel()
		return nil, err
	}
	go c.reconnectLoop()
	return c, nil
}

func (c *Client) Events() <-chan Event { return c.events }

func (c *Client) Close() error {
	c.cancel()
	c.connMu.Lock()
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()
	if conn != nil {
		return conn.Close(websocket.StatusNormalClosure, "closed")
	}
	return nil
}

func (c *Client) ChatList(ctx context.Context, afterID int64, limit int) ([]ChatMessage, bool, error) {
	var out ChatListResult
	err := c.rpc(ctx, "chat.list", map[string]any{"after_id": afterID, "limit": limit}, &out)
	return out.Messages, out.HasMore, err
}

func (c *Client) ChatSend(ctx context.Context, content, topic string, replyTo int64) error {
	payload := map[string]any{"content": content, "topic": topic}
	if replyTo > 0 {
		payload["reply_to"] = replyTo
	}
	return c.send(ctx, "chat.send", payload)
}

func (c *Client) RosterList(ctx context.Context) ([]RosterAspect, error) {
	var out frames.RosterListResultPayload
	if err := c.rpc(ctx, "roster.list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Aspects, nil
}

func (c *Client) RunsList(ctx context.Context, limit int) ([]Run, error) {
	var out struct {
		Runs []Run `json:"runs"`
	}
	if err := c.rpc(ctx, "runs.list", map[string]any{"limit": limit}, &out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}

func (c *Client) Subscribe(ctx context.Context, kinds ...string) error {
	for _, kind := range kinds {
		raw := json.RawMessage(`{}`)
		c.subMu.Lock()
		c.subscriptions[kind] = raw
		c.subMu.Unlock()
		if err := c.sendRaw(ctx, kind, raw); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) rpc(ctx context.Context, kind string, payload any, dst any) error {
	env, err := frames.NewRequest(frames.Kind(kind), payload)
	if err != nil {
		return err
	}
	ch := make(chan rpcResult, 1)
	c.pendingMu.Lock()
	c.pending[env.ID] = rpcPending{wantKind: frames.Kind(kind + ".result"), ch: ch}
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, env.ID)
		c.pendingMu.Unlock()
	}()
	if err := c.writeEnvelope(ctx, env); err != nil {
		return err
	}
	timeout := c.cfg.RPCDeadline
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-rctx.Done():
		return fmt.Errorf("opclient: rpc %s: %w", kind, rctx.Err())
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if strings.HasSuffix(string(res.env.Kind), ".error") {
			return fmt.Errorf("opclient: rpc %s: %s", kind, string(res.env.Payload))
		}
		if dst == nil {
			return nil
		}
		return frames.PayloadAs(res.env, dst)
	}
}

func (c *Client) send(ctx context.Context, kind string, payload any) error {
	env, err := frames.NewRequest(frames.Kind(kind), payload)
	if err != nil {
		return err
	}
	return c.writeEnvelope(ctx, env)
}

func (c *Client) sendRaw(ctx context.Context, kind string, raw json.RawMessage) error {
	env := frames.Envelope{Kind: frames.Kind(kind), ID: mustRequestID(), TS: time.Now().UTC(), Payload: raw}
	return c.writeEnvelope(ctx, env)
}

func (c *Client) writeEnvelope(ctx context.Context, env frames.Envelope) error {
	raw, err := frames.Encode(env)
	if err != nil {
		return err
	}
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return errors.New("opclient: websocket not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, c.cfg.RPCDeadline)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, raw)
}

func (c *Client) connect(ctx context.Context) error {
	wsURL, err := c.wsURL()
	if err != nil {
		return err
	}
	opts := &websocket.DialOptions{}
	if c.tlsConfig != nil {
		opts.HTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: c.tlsConfig}}
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, opts)
	if err != nil {
		return fmt.Errorf("opclient: dial: %w", err)
	}
	conn.SetReadLimit(1 << 20)
	c.connMu.Lock()
	c.conn = conn
	c.readDone = make(chan error, 1)
	done := c.readDone
	c.connMu.Unlock()
	c.emit(ConnState{Connected: true})
	go c.readLoop(conn, done)
	return nil
}

func (c *Client) readLoop(conn *websocket.Conn, done chan<- error) {
	var ret error
	defer func() {
		c.connMu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.connMu.Unlock()
		c.failPending(ret)
		c.emit(ConnState{Connected: false, Err: ret})
		done <- ret
	}()
	for {
		typ, data, err := conn.Read(c.ctx)
		if err != nil {
			ret = err
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		env, err := frames.Decode(data)
		if err != nil {
			continue
		}
		c.demux(env)
	}
}

func (c *Client) demux(env frames.Envelope) {
	if env.InReplyTo != "" {
		c.pendingMu.Lock()
		pending, ok := c.pending[env.InReplyTo]
		c.pendingMu.Unlock()
		if ok && (env.Kind == pending.wantKind || strings.HasSuffix(string(env.Kind), ".error")) {
			pending.ch <- rpcResult{env: env}
			return
		}
	}
	if strings.HasSuffix(string(env.Kind), ".result") && env.ID != "" {
		c.pendingMu.Lock()
		pending, ok := c.pending[env.ID]
		c.pendingMu.Unlock()
		if ok && env.Kind == pending.wantKind {
			pending.ch <- rpcResult{env: env}
			return
		}
	}

	switch string(env.Kind) {
	case "chat.update", "chat.deliver":
		var msg ChatMessage
		if err := frames.PayloadAs(env, &msg); err == nil {
			c.deliverMsg(msg)
		}
	case "runs.update":
		run := Run{Raw: append(json.RawMessage(nil), env.Payload...)}
		_ = json.Unmarshal(env.Payload, &run)
		c.emit(RunEvent{Run: run})
	case "escalation.request":
		var ev EscalationEvent
		if err := frames.PayloadAs(env, &ev); err == nil {
			ev.RequestID = env.ID
			c.emit(ev)
		}
	default:
	}
}

func (c *Client) reconnectLoop() {
	for {
		c.connMu.RLock()
		done := c.readDone
		c.connMu.RUnlock()
		select {
		case <-c.ctx.Done():
			return
		case <-done:
		}
		backoff := c.cfg.ReconnectMin
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(backoff):
			}
			if err := c.authProbe(c.ctx); err != nil {
				backoff = nextBackoff(backoff, c.cfg.ReconnectMax)
				continue
			}
			if err := c.connect(c.ctx); err != nil {
				backoff = nextBackoff(backoff, c.cfg.ReconnectMax)
				continue
			}
			c.replayAfterReconnect()
			break
		}
	}
}

func (c *Client) replayAfterReconnect() {
	c.subMu.Lock()
	subs := make(map[string]json.RawMessage, len(c.subscriptions))
	for k, v := range c.subscriptions {
		subs[k] = append(json.RawMessage(nil), v...)
	}
	c.subMu.Unlock()
	c.cursorMu.Lock()
	after := c.lastSeenID
	c.cursorMu.Unlock()
	for kind, raw := range subs {
		_ = c.sendRaw(c.ctx, kind, raw)
	}
	if after <= 0 {
		return
	}
	for {
		msgs, hasMore, err := c.ChatList(c.ctx, after, 200)
		if err != nil {
			return
		}
		for _, msg := range msgs {
			c.deliverMsg(msg)
			if msg.ID > after {
				after = msg.ID
			}
		}
		if !hasMore || len(msgs) == 0 {
			return
		}
	}
}

func (c *Client) deliverMsg(msg ChatMessage) {
	if msg.ID > 0 {
		c.cursorMu.Lock()
		if msg.ID <= c.cursorBase {
			c.cursorMu.Unlock()
			return
		}
		if _, ok := c.seen[msg.ID]; ok {
			c.cursorMu.Unlock()
			return
		}
		c.seen[msg.ID] = struct{}{}
		if msg.ID > c.lastSeenID {
			c.lastSeenID = msg.ID
			_ = c.persistCursorLocked()
		}
		c.cursorMu.Unlock()
	}
	c.emit(MsgEvent{Message: msg})
}

func (c *Client) authProbe(ctx context.Context) error {
	u, err := url.Parse(c.cfg.BrokerURL)
	if err != nil {
		return fmt.Errorf("opclient: parse broker url: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/auth/mode"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("opclient: auth mode: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("opclient: auth mode status %s", resp.Status)
	}
	var mode struct {
		Bypass bool `json:"bypass"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mode); err != nil {
		return fmt.Errorf("opclient: auth mode decode: %w", err)
	}
	if !mode.Bypass && c.cfg.Token == "" {
		return errors.New("opclient: broker requires token")
	}
	return nil
}

func (c *Client) wsURL() (string, error) {
	u, err := url.Parse(c.cfg.BrokerURL)
	if err != nil {
		return "", fmt.Errorf("opclient: parse broker url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("opclient: unsupported broker scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/connect"
	q := u.Query()
	q.Set("token", c.cfg.Token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Client) failPending(err error) {
	if err == nil {
		err = errors.New("opclient: connection closed")
	}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, pending := range c.pending {
		delete(c.pending, id)
		pending.ch <- rpcResult{err: err}
	}
}

func (c *Client) emit(ev Event) {
	select {
	case c.events <- ev:
	case <-c.ctx.Done():
	}
}

func (c *Client) loadCursor() int64 {
	data, err := os.ReadFile(c.cursorFile)
	if err != nil {
		return 0
	}
	var state struct {
		LastSeenID int64 `json:"last_seen_id"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return 0
	}
	return state.LastSeenID
}

func (c *Client) persistCursorLocked() error {
	state := struct {
		LastSeenID int64 `json:"last_seen_id"`
	}{LastSeenID: c.lastSeenID}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := c.cursorFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.cursorFile)
}

func tlsConfigFromPinnedCert(pem string) (*tls.Config, error) {
	if pem == "" {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, errors.New("opclient: pinned cert is not valid PEM")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func httpClientWithTLS(tlsCfg *tls.Config) *http.Client {
	if tlsCfg == nil {
		return &http.Client{Timeout: 10 * time.Second}
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	cur *= 2
	if cur > max {
		return max
	}
	return cur
}

func mustRequestID() string {
	env, err := frames.NewRequest("noop", nil)
	if err != nil {
		panic(err)
	}
	return env.ID
}
