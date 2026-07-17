package mcp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// stubClient is an in-process fake connected server — no process spawn, no
// network, per the DoD's "use an in-process STUB MCP server for tests".
type stubClient struct {
	tools []contracts.ToolSpec
}

func (c *stubClient) ListTools(ctx context.Context) ([]contracts.ToolSpec, error) {
	return c.tools, nil
}
func (c *stubClient) Close() error { return nil }

// stubConnector controls per-server connect behavior deterministically:
// immediate success, immediate failure (incl. auth-required), or a delay
// gated by a channel the test controls (never a real sleep).
type stubConnector struct {
	behaviors map[string]func() (Client, error)
	callCount map[string]*int32
	// ctxBehaviors, when set for a server name, takes priority over
	// behaviors and receives the context passed to Connect — used by tests
	// that must observe ctx cancellation reaching the connector.
	ctxBehaviors map[string]func(ctx context.Context) (Client, error)
}

func newStubConnector() *stubConnector {
	return &stubConnector{behaviors: map[string]func() (Client, error){}, callCount: map[string]*int32{}}
}

func (s *stubConnector) set(name string, fn func() (Client, error)) {
	s.behaviors[name] = fn
	var n int32
	s.callCount[name] = &n
}

func (s *stubConnector) Connect(ctx context.Context, cfg ServerConfig) (Client, error) {
	if fn, ok := s.ctxBehaviors[cfg.Name]; ok {
		return fn(ctx)
	}
	if fn, ok := s.behaviors[cfg.Name]; ok {
		if c := s.callCount[cfg.Name]; c != nil {
			atomic.AddInt32(c, 1)
		}
		return fn()
	}
	return &stubClient{}, nil
}

func TestManager_RequiredServerFailureIsError(t *testing.T) {
	conn := newStubConnector()
	conn.set("req", func() (Client, error) { return nil, errors.New("boom") })
	m := NewManager(conn)

	cfgs := []ServerConfig{
		{Name: "req", Enabled: true, Required: true, StartupTimeout: time.Second, Command: "x"},
	}
	res, events, err := m.StartAll(context.Background(), cfgs)
	drain(events)
	if err == nil {
		t.Fatalf("expected error for failed required server")
	}
	if !errors.Is(err, ErrRequiredServerFailed) {
		t.Fatalf("err = %v, want ErrRequiredServerFailed", err)
	}
	if _, failed := res.Failed["req"]; !failed {
		t.Fatalf("expected req in Failed: %+v", res)
	}
}

func TestManager_OptionalServerFailureIsNotError(t *testing.T) {
	conn := newStubConnector()
	conn.set("opt", func() (Client, error) { return nil, errors.New("boom") })
	m := NewManager(conn)

	cfgs := []ServerConfig{
		{Name: "opt", Enabled: true, Required: false, StartupTimeout: time.Second, Command: "x"},
	}
	res, events, err := m.StartAll(context.Background(), cfgs)
	drain(events)
	if err != nil {
		t.Fatalf("expected no error for optional server failure, got %v", err)
	}
	if _, failed := res.Failed["opt"]; !failed {
		t.Fatalf("expected opt recorded as failed: %+v", res)
	}
}

func TestManager_DisabledServerSkippedEntirely(t *testing.T) {
	conn := newStubConnector()
	m := NewManager(conn)
	cfgs := []ServerConfig{
		{Name: "off", Enabled: false, Required: true, Command: "x"},
	}
	res, events, err := m.StartAll(context.Background(), cfgs)
	drain(events)
	if err != nil {
		t.Fatalf("disabled required server must not error: %v", err)
	}
	if len(res.Ready) != 0 || len(res.Failed) != 0 || len(res.Cancelled) != 0 {
		t.Fatalf("expected disabled server to produce no result: %+v", res)
	}
}

func TestManager_StartupTimeoutHonored(t *testing.T) {
	conn := newStubConnector()
	block := make(chan struct{})
	conn.set("slow", func() (Client, error) {
		<-block // never closed in this test: forces the manager's timeout path
		return &stubClient{}, nil
	})
	m := NewManager(conn)
	cfgs := []ServerConfig{
		{Name: "slow", Enabled: true, Required: true, StartupTimeout: 20 * time.Millisecond, Command: "x"},
	}
	res, events, err := m.StartAll(context.Background(), cfgs)
	drain(events)
	if err == nil {
		t.Fatalf("expected required-server timeout to error")
	}
	if e, ok := res.Failed["slow"]; !ok || !errors.Is(e, ErrStartupTimeout) {
		t.Fatalf("expected ErrStartupTimeout, got %v", res.Failed["slow"])
	}
	close(block)
}

// TestManager_TimeoutCancelsInFlightConnect asserts that a startOne
// timeout actually signals the underlying Connect's context, rather than
// merely abandoning it locally while the background goroutine (and any
// subprocess/dial it owns) leaks forever. The stub Connect blocks on
// ctx.Done() (never on a channel the test controls directly) and reports
// back over cancelled — if that never fires, ctx was never cancelled.
func TestManager_TimeoutCancelsInFlightConnect(t *testing.T) {
	conn := newStubConnector()
	cancelled := make(chan struct{})
	conn.ctxBehaviors = map[string]func(ctx context.Context) (Client, error){
		"slow": func(ctx context.Context) (Client, error) {
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
	}

	m := NewManager(conn)
	cfgs := []ServerConfig{
		{Name: "slow", Enabled: true, Required: true, StartupTimeout: 20 * time.Millisecond, Command: "x"},
	}
	res, events, err := m.StartAll(context.Background(), cfgs)
	drain(events)
	if err == nil {
		t.Fatalf("expected required-server timeout to error")
	}
	if _, ok := res.Failed["slow"]; !ok {
		t.Fatalf("expected slow in Failed: %+v", res)
	}

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatalf("startOne timeout did not cancel the in-flight Connect's context (goroutine leak)")
	}
}

// TestManager_CancelStopsInFlightConnect exercises the explicit Cancel(name)
// path: it must invoke the stored cancel func for that server's future, not
// just delete the map entry, so the background goroutine actually unblocks.
func TestManager_CancelStopsInFlightConnect(t *testing.T) {
	conn := newStubConnector()
	cancelled := make(chan struct{})
	started := make(chan struct{})
	conn.ctxBehaviors = map[string]func(ctx context.Context) (Client, error){
		"herald": func(ctx context.Context) (Client, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
	}
	m := NewManager(conn)
	f := m.connect(ServerConfig{Name: "herald", Command: "x"})
	<-started
	m.Cancel("herald")
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatalf("Cancel(name) did not cancel the in-flight Connect's context (goroutine leak)")
	}
	<-f.ready
}

func TestManager_AuthRequiredSurfacesHint(t *testing.T) {
	conn := newStubConnector()
	conn.set("herald", func() (Client, error) { return nil, ErrAuthRequired })
	m := NewManager(conn)
	cfgs := []ServerConfig{
		{Name: "herald", Enabled: true, Required: false, StartupTimeout: time.Second, Command: "x"},
	}
	res, events, _ := m.StartAll(context.Background(), cfgs)
	drain(events)
	err := res.Failed["herald"]
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("expected ErrAuthRequired, got %v", err)
	}
	hint := StartupHint("herald", err)
	if hint == "" {
		t.Fatalf("expected a non-empty auth hint")
	}
}

func TestManager_SharedFutureNeverReconnects(t *testing.T) {
	conn := newStubConnector()
	conn.set("once", func() (Client, error) { return &stubClient{tools: []contracts.ToolSpec{{Name: "t"}}}, nil })
	m := NewManager(conn)
	cfg := ServerConfig{Name: "once", Enabled: true, Required: true, StartupTimeout: time.Second, Command: "x"}

	res1, ev1, err1 := m.StartAll(context.Background(), []ServerConfig{cfg})
	drain(ev1)
	if err1 != nil || len(res1.Ready) != 1 {
		t.Fatalf("first StartAll: res=%+v err=%v", res1, err1)
	}
	// Second StartAll for the same server must await the SAME future, not
	// reconnect (§2: "awaiting it never re-connects").
	res2, ev2, err2 := m.StartAll(context.Background(), []ServerConfig{cfg})
	drain(ev2)
	if err2 != nil || len(res2.Ready) != 1 {
		t.Fatalf("second StartAll: res=%+v err=%v", res2, err2)
	}
	if n := atomic.LoadInt32(conn.callCount["once"]); n != 1 {
		t.Fatalf("connector.Connect called %d times, want 1", n)
	}

	c, ok := m.Client("once")
	if !ok {
		t.Fatalf("expected Client() to find the settled future")
	}
	tools, _ := c.ListTools(context.Background())
	if len(tools) != 1 || tools[0].Name != "t" {
		t.Fatalf("ListTools = %+v", tools)
	}
}

func TestManager_MultipleServersConcurrent(t *testing.T) {
	conn := newStubConnector()
	conn.set("a", func() (Client, error) { return &stubClient{}, nil })
	conn.set("b", func() (Client, error) { return nil, errors.New("b failed") })
	m := NewManager(conn)
	cfgs := []ServerConfig{
		{Name: "a", Enabled: true, Required: false, StartupTimeout: time.Second, Command: "x"},
		{Name: "b", Enabled: true, Required: false, StartupTimeout: time.Second, Command: "x"},
	}
	res, events, err := m.StartAll(context.Background(), cfgs)
	drain(events)
	if err != nil {
		t.Fatalf("no required server, expected no error: %v", err)
	}
	if len(res.Ready) != 1 || res.Ready[0] != "a" {
		t.Fatalf("Ready = %v", res.Ready)
	}
	if _, ok := res.Failed["b"]; !ok {
		t.Fatalf("expected b failed: %+v", res)
	}
}

func drain(events <-chan StartupEvent) []StartupEvent {
	var out []StartupEvent
	for e := range events {
		out = append(out, e)
	}
	return out
}
