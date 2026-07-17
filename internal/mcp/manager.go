package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// ServerState is a server's position in the §2 startup lifecycle:
// Starting -> Ready/Failed/Cancelled.
type ServerState string

const (
	StateStarting  ServerState = "starting"
	StateReady     ServerState = "ready"
	StateFailed    ServerState = "failed"
	StateCancelled ServerState = "cancelled"
)

// Client is a connected server handle. The real MCP wire protocol (stdio
// framing / streamable_http) is owned by the go-sdk per the spec's header
// note; this package depends only on this seam, so tests exercise it
// against an in-process stub, never a spawned process or a network dial.
type Client interface {
	ListTools(ctx context.Context) ([]contracts.ToolSpec, error)
	Close() error
}

// Connector opens one server connection. Production wiring (stdio subprocess,
// streamable_http dial) is another unit's concern; Manager only needs this
// seam.
type Connector interface {
	Connect(ctx context.Context, cfg ServerConfig) (Client, error)
}

// StartupEvent is one Starting/Ready/Failed/Cancelled transition, emitted on
// the Manager's event channel for the UI (§2).
type StartupEvent struct {
	Server string
	State  ServerState
	Err    error
}

// AggregateResult is the final {ready, failed, cancelled} summary §2 asks
// for after eager concurrent startup completes.
type AggregateResult struct {
	Ready     []string
	Failed    map[string]error
	Cancelled []string
}

// future is the cached shared connect future for one server name: "the
// client handle is a cached shared connect future — awaiting it never
// re-connects" (§2). Built once per Manager per server name, regardless of
// how many callers/Start invocations await it.
type future struct {
	ready  chan struct{}
	client Client
	err    error
}

// Manager runs eager concurrent MCP server startup and required-server
// gating (§2). It does not itself implement the wire protocol — it drives a
// Connector and applies the policy (timeouts, required gating, shared
// futures, event emission) around it.
type Manager struct {
	connector Connector

	mu      sync.Mutex
	futures map[string]*future
}

// NewManager builds a Manager over connector.
func NewManager(connector Connector) *Manager {
	return &Manager{connector: connector, futures: make(map[string]*future)}
}

// connect returns cfg's shared future, starting the connector exactly once
// per server name. cfg from the FIRST caller wins the actual connect
// (subsequent callers for the same name are assumed to pass an equivalent
// config — the manager does not re-validate on every await).
func (m *Manager) connect(cfg ServerConfig) *future {
	m.mu.Lock()
	f, ok := m.futures[cfg.Name]
	if ok {
		m.mu.Unlock()
		return f
	}
	f = &future{ready: make(chan struct{})}
	m.futures[cfg.Name] = f
	m.mu.Unlock()

	go func() {
		defer close(f.ready)
		// The connect itself runs unbounded on a background context — a
		// caller's local timeout (Start's per-server select) governs
		// whether THAT caller waits for it, but does not tear down a
		// connect another caller may still be awaiting (that is what
		// Cancel/Shutdown is for).
		f.client, f.err = m.connector.Connect(context.Background(), cfg)
	}()
	return f
}

// Cancel tears down a specific server's future's context by discarding it
// so a future StartAll call reconnects. It does not forcibly interrupt an
// in-flight Connect (Connector implementations are expected to honor ctx
// cancellation themselves for that); it is the per-server token §2 asks for
// at the bookkeeping level this package owns.
func (m *Manager) Cancel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.futures, name)
}

// StartAll starts every enabled server in cfgs concurrently (one goroutine
// each, §2 "eager concurrent startup"), emits a StartupEvent for each
// state transition on the returned channel (closed when all servers have
// settled), and returns the aggregate result plus a non-nil error iff at
// least one `required` server failed or was cancelled (§2 required-server
// gating; ErrRequiredServerFailed wraps the per-server errors).
//
// Disabled servers (Enabled == false) are skipped entirely — never started,
// never reported (§1: "false ⇒ skipped entirely").
func (m *Manager) StartAll(ctx context.Context, cfgs []ServerConfig) (AggregateResult, <-chan StartupEvent, error) {
	events := make(chan StartupEvent, len(cfgs)*2+1)

	var wg sync.WaitGroup
	var mu sync.Mutex
	result := AggregateResult{Failed: make(map[string]error)}

	for _, cfg := range cfgs {
		if !cfg.Enabled {
			continue
		}
		cfg := cfg
		wg.Add(1)
		events <- StartupEvent{Server: cfg.Name, State: StateStarting}
		go func() {
			defer wg.Done()
			state, err := m.startOne(ctx, cfg)
			mu.Lock()
			switch state {
			case StateReady:
				result.Ready = append(result.Ready, cfg.Name)
			case StateFailed:
				result.Failed[cfg.Name] = err
			case StateCancelled:
				result.Cancelled = append(result.Cancelled, cfg.Name)
			}
			mu.Unlock()
			events <- StartupEvent{Server: cfg.Name, State: state, Err: err}
		}()
	}

	go func() {
		wg.Wait()
		close(events)
	}()

	// Await settlement synchronously (callers wanting live progress read
	// `events` concurrently; the channel is buffered generously enough that
	// this drain never blocks a slow consumer for the aggregate return).
	wg.Wait()

	var aggErr error
	var failedRequired []string
	for _, cfg := range cfgs {
		if !cfg.Enabled || !cfg.Required {
			continue
		}
		if _, failed := result.Failed[cfg.Name]; failed {
			failedRequired = append(failedRequired, cfg.Name)
			continue
		}
		if containsStr(result.Cancelled, cfg.Name) {
			failedRequired = append(failedRequired, cfg.Name)
		}
	}
	if len(failedRequired) > 0 {
		var msgs []string
		for _, n := range failedRequired {
			if e, ok := result.Failed[n]; ok {
				msgs = append(msgs, fmt.Sprintf("%s: %v", n, e))
			} else {
				msgs = append(msgs, fmt.Sprintf("%s: cancelled", n))
			}
		}
		aggErr = fmt.Errorf("%w: %v", ErrRequiredServerFailed, msgs)
	}

	return result, events, aggErr
}

// startOne runs one server's Starting -> Ready/Failed/Cancelled transition,
// honoring cfg.StartupTimeout and ctx cancellation.
func (m *Manager) startOne(ctx context.Context, cfg ServerConfig) (ServerState, error) {
	timeout := cfg.StartupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	f := m.connect(cfg)
	select {
	case <-f.ready:
		if f.err != nil {
			if errors.Is(f.err, ErrAuthRequired) {
				return StateFailed, f.err
			}
			return StateFailed, f.err
		}
		return StateReady, nil
	case <-timer.C:
		return StateFailed, fmt.Errorf("%w: server %q after %s", ErrStartupTimeout, cfg.Name, timeout)
	case <-ctx.Done():
		return StateCancelled, ctx.Err()
	}
}

// Client returns the shared connected Client for name, if its future has
// settled successfully. Awaiting an already-settled future never
// reconnects (§2).
func (m *Manager) Client(name string) (Client, bool) {
	m.mu.Lock()
	f, ok := m.futures[name]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	<-f.ready
	if f.err != nil {
		return nil, false
	}
	return f.client, true
}

// StartupHint returns the §2 "special-case" UX string for a startup error,
// or "" for errors with no special-cased hint (caller falls back to err.Error()).
func StartupHint(server string, err error) string {
	switch {
	case errors.Is(err, ErrAuthRequired):
		return fmt.Sprintf("run `agora mcp login %s`", server)
	case errors.Is(err, ErrStartupTimeout):
		return fmt.Sprintf("bump startup_timeout_sec for %s", server)
	default:
		return ""
	}
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
