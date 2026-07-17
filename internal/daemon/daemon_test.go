package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
)

func fixedClock(t time.Time) Clock { return func() time.Time { return t } }

// shortSockPath mirrors internal/io/transport_test.go's helper (AF_UNIX
// sun_path length backstop on macOS).
func shortSockPath(t *testing.T, name string) string {
	t.Helper()
	base := ""
	if runtime.GOOS == "darwin" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "agdaemon")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

func constFactory(e agoraio.Engine) EngineFactory {
	return func(string, contracts.ThreadMeta) agoraio.Engine { return e }
}

// TestSession_MintsOnce_UnknownThreadRefused proves the SessionLookup seam:
// a thread the store has never heard of is refused, and repeated lookups of
// a known thread return the SAME Session (not a fresh one per call).
func TestSession_MintsOnce_UnknownThreadRefused(t *testing.T) {
	ctx := context.Background()
	engine := &agoraio.ScriptedEngine{}
	d := NewDaemon(ctx, Config{Clock: fixedClock(time.Unix(0, 0)), EngineFactory: constFactory(engine)})

	if _, err := d.Session("th_unknown"); err == nil {
		t.Fatal("Session on an unregistered thread succeeded, want ErrUnknownThread")
	}

	id, err := d.CreateThread(contracts.ThreadMeta{ThreadID: "th_known", Profile: "dev"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	s1, err := d.Session(id)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	s2, err := d.Session(id)
	if err != nil {
		t.Fatalf("Session (again): %v", err)
	}
	if s1 != s2 {
		t.Fatal("Session minted a second Session for the same thread id")
	}
	d.Close()
}

// TestServeUnix_TwoRealClients_FanOutAndByAttribution drives the real
// session-protocol wire (ServeUnix + two internal/tui.DialUnixBackend
// clients — the production client) over an actual unix socket, proving:
// both authenticated (registry-enrolled) devices' AttachInfo comes from
// the registry (not the wire), fan-out reaches both, and the daemon's
// by-attribution side-channel resolves to the winning client's device id
// after a real first-answer-wins race decided by io.Session.
func TestServeUnix_TwoRealClients_FanOutAndByAttribution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := remote.NewRegistry(nil)
	if _, err := registry.Enroll("dev-a", nil, remote.DeviceMetadata{}, []contracts.Capability{contracts.CapInteractive, contracts.CapApprover}); err != nil {
		t.Fatalf("enroll dev-a: %v", err)
	}
	if _, err := registry.Enroll("dev-b", nil, remote.DeviceMetadata{}, []contracts.Capability{contracts.CapInteractive, contracts.CapApprover}); err != nil {
		t.Fatalf("enroll dev-b: %v", err)
	}

	var d *Daemon
	factory := func(threadID string, meta contracts.ThreadMeta) agoraio.Engine {
		return &awaitApprovalEngine{daemon: func() *Daemon { return d }, threadID: threadID}
	}
	d = NewDaemon(ctx, Config{Clock: fixedClock(time.Unix(0, 0)), Registry: registry, EngineFactory: factory})
	defer d.Close()

	threadID, err := d.CreateThread(contracts.ThreadMeta{ThreadID: "th_wire", Profile: "dev"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	sockPath := shortSockPath(t, "daemon.sock")
	ln, err := agoraio.ListenUnix(sockPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	go func() { _ = d.ServeUnix(ctx, ln) }()

	backendA, err := tui.DialUnixBackend(sockPath, agoraio.AttachRequest{ThreadID: threadID, ClientID: "dev-a", Kind: "tui", Replay: 16})
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer backendA.Close()
	backendB, err := tui.DialUnixBackend(sockPath, agoraio.AttachRequest{ThreadID: threadID, ClientID: "dev-b", Kind: "tui", Replay: 16})
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer backendB.Close()

	// Kick the turn so the engine raises approval.requested.
	if err := backendA.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "go"}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}

	waitFor(t, backendA, contracts.EvApprovalRequested)
	waitFor(t, backendB, contracts.EvApprovalRequested)

	// Both genuinely race an approval_response for ap_0001, with DIFFERENT
	// decisions (A=allow, B=deny) so the winner is externally observable
	// from the resolved decision, not assumed. Which one wins the race is
	// legitimately non-deterministic (io.Session's first-answer-wins
	// arbitration, session.go) — this test does not assume A wins; it
	// asserts that whichever one wins, BOTH clients see the SAME resolution
	// (one-and-only-one full resolution per id, attributed to the actual
	// winner via the daemon's real by-attribution side-channel), not that a
	// specific device always does.
	sendErrs := make(chan error, 2)
	go func() {
		sendErrs <- backendB.Send(ctx, contracts.Input{Type: contracts.InApprovalResponse, ID: "ap_0001", Decision: contracts.DecisionDeny})
	}()
	go func() {
		sendErrs <- backendA.Send(ctx, contracts.Input{Type: contracts.InApprovalResponse, ID: "ap_0001", Decision: contracts.DecisionAllow, Scope: contracts.ScopeOnce})
	}()
	for range 2 {
		if err := <-sendErrs; err != nil {
			t.Fatalf("send approval_response: %v", err)
		}
	}

	wantByDecision := map[string]contracts.Decision{"dev-a": contracts.DecisionAllow, "dev-b": contracts.DecisionDeny}
	resA := waitForResolution(t, backendA, "ap_0001")
	resB := waitForResolution(t, backendB, "ap_0001")
	if resA != resB {
		t.Fatalf("both clients must see the SAME resolution: A=%+v B=%+v", resA, resB)
	}
	if want, ok := wantByDecision[resA.By]; !ok || resA.Decision != want {
		t.Fatalf("resolution %+v not attributed to a real racer with its own decision", resA)
	}
}

func waitFor(t *testing.T, b tui.Backend, want contracts.EventType) contracts.Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-b.Events():
			if ev.Type == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

// waitForResolution scans b's event stream for the FULL approval.resolved
// (contracts.ApprovalResolution, By+Stage set) for id — ignoring the
// session's own thin {id,by} broadcast, which decodes into the same struct
// shape but with Stage empty (blueprint §6 resolution 2: "treat the thin
// event as ignorable extra").
func waitForResolution(t *testing.T, b tui.Backend, id string) contracts.ApprovalResolution {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-b.Events():
			if ev.Type != contracts.EvApprovalResolved {
				continue
			}
			var res contracts.ApprovalResolution
			if err := json.Unmarshal(ev.Payload, &res); err != nil {
				t.Fatalf("decode approval.resolved: %v", err)
			}
			if res.ID != id || res.Stage == "" {
				continue // the thin {id,by} broadcast — Stage is unset on it
			}
			return res
		case <-deadline:
			t.Fatalf("timed out waiting for the full approval.resolved for %s", id)
		}
	}
}

// awaitApprovalEngine is a minimal test Engine: on the first Input it
// raises approval.requested{ap_0001,kind=exec}; on the matching
// approval_response Input it resolves via d.WaitForBy (the real daemon
// side-channel, not a canned value) + daemon.ResolveApproval (the real
// internal/approval.Result.Resolution conversion) and emits the full
// approval.resolved.
type awaitApprovalEngine struct {
	daemon   func() *Daemon
	threadID string
}

func (e *awaitApprovalEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	first := true
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case i, ok := <-in:
			if !ok {
				return nil
			}
			if first {
				first = false
				payload, _ := json.Marshal(contracts.ApprovalRequest{ID: "ap_0001", Kind: contracts.KindExec, Payload: map[string]string{"command": "go test"}})
				select {
				case out <- contracts.Event{Type: contracts.EvApprovalRequested, ThreadID: e.threadID, Payload: payload}:
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			if i.Type != contracts.InApprovalResponse || i.ID != "ap_0001" {
				continue
			}
			by, err := e.daemon().WaitForBy(ctx, i.ID)
			if err != nil {
				return err
			}
			res := ResolveApproval(i.ID, contracts.KindExec, i, by)
			payload, _ := json.Marshal(res)
			select {
			case out <- contracts.Event{Type: contracts.EvApprovalResolved, ThreadID: e.threadID, Payload: payload}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// stubRunner is a minimal subagent.AgentRunner for the handoff test — agent
// EXECUTION is out of this unit's scope (subagent/doc.go); the test only
// needs Spawn to run to completion without erroring on tools.
type stubRunner struct {
	captured chan subagent.RunRequest
}

func (r *stubRunner) Run(ctx context.Context, req subagent.RunRequest) (subagent.RunResult, error) {
	if r.captured != nil {
		r.captured <- req
	}
	return subagent.RunResult{Output: json.RawMessage(`{"ok":true}`)}, nil
}

// TestSubagentSpawnAfterCreateThread is the U10 handoff (blueprint §4):
// CreateThread MUST RegisterRoot the thread with the subagent manager
// before any agent() spawn could occur — a thread that spawns via an
// UNREGISTERED parent fails closed on tools (subagent/manager.go's own
// FIX 5 rule: Tools becomes []string{}, "no tools"). This drives a REAL
// subagent.Manager.Spawn call right after CreateThread and asserts the
// spawn's resolved tool set is NOT the fail-closed empty set.
func TestSubagentSpawnAfterCreateThread(t *testing.T) {
	ctx := context.Background()
	captured := make(chan subagent.RunRequest, 1)
	runner := &stubRunner{captured: captured}

	store := persistence.NewMemStore()
	d := NewDaemon(ctx, Config{
		Clock:         fixedClock(time.Unix(0, 0)),
		Store:         store,
		EngineFactory: constFactory(&agoraio.ScriptedEngine{}),
		// A capturing runner wired in explicitly: NewDaemon's default
		// noopRunner refuses every Run call, which would make this test
		// unable to observe the resolved tool set.
		Subagents: subagent.NewManager(store, subagent.NewMemGraphStore(), subagent.NewRegistry(nil), runner),
	})

	threadID, err := d.CreateThread(contracts.ThreadMeta{ThreadID: "th_root", Profile: "dev"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if _, err := d.Subagents().Spawn(ctx, threadID, "do a thing", subagent.SpawnOpts{Foreground: true}); err != nil {
		t.Fatalf("Spawn after CreateThread failed closed: %v", err)
	}

	select {
	case req := <-captured:
		if req.Tools != nil && len(req.Tools) == 0 {
			t.Fatalf("spawn from a CreateThread-registered root failed closed on tools: %+v", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runner never invoked (Spawn always runs the runner in its own goroutine, spec §2)")
	}
}
