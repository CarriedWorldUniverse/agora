package conformance

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/daemon"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
)

// sessionAwaitEngine is the flowEngine wrapper used by the 3.2b sub-drive:
// identical shape to the pipe drive's flowEngine, except its Resolve
// closure attributes via d.WaitForBy(ctx, id) — the REAL daemon
// by-attribution side-channel resolved from an actual first-answer-wins
// race between two real session-protocol clients, not a fixed constant
// (pipe mode has no such race to resolve, §3.2a). resolveCalls counts how
// many times THIS Resolve closure actually ran (the engine-side proof that
// the seam resolved exactly once, independent of how many attached clients
// later observe the one resulting broadcast event).
func newSessionApprovalEngine(d func() *daemon.Daemon, threadID, turnID string, resolveCalls *int64) *flowEngine {
	return &flowEngine{steps: []awaitStep{
		{Emit: []contracts.Event{
			{Type: contracts.EvApprovalRequested, ThreadID: threadID, TurnID: turnID, Payload: mustMarshalJSON(contracts.ApprovalRequest{
				ID: "ap_0001", Kind: contracts.KindExec, Payload: execCommandPayload{Command: "go test ./..."},
			})},
		}},
		{
			Await: contracts.InApprovalResponse, AwaitID: "ap_0001",
			Resolve: func(in contracts.Input) ([]contracts.Event, error) {
				atomic.AddInt64(resolveCalls, 1)
				by, err := d().WaitForBy(context.Background(), "ap_0001")
				if err != nil {
					return nil, err
				}
				res := daemon.ResolveApproval("ap_0001", contracts.KindExec, in, by)
				return []contracts.Event{{Type: contracts.EvApprovalResolved, ThreadID: threadID, TurnID: turnID, Payload: mustMarshalJSON(res)}}, nil
			},
		},
	}}
}

// TestFlowApproval_SessionProtocolFanOut is the second half of TestFlowApproval
// (blueprint §3.2b): boots a real daemon over a real unix socket, dials TWO
// real internal/tui clients (the production Backend), races a genuine
// first-answer-wins approval_response, and asserts BOTH clients observe the
// SAME full resolution attributed to the real winner (FIELDS, not a byte
// match — §6 resolution 2: session mode also broadcasts a thin {id,by}
// event the drive must treat as an ignorable extra). It then extends with a
// THIRD device whose AllowedThreads excludes the thread, asserting the U16
// CheckThread handoff refuses the attach (blueprint §4).
func sessionProtocolFanOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := remote.NewRegistry(nil)
	for _, id := range []string{"dev-a", "dev-b"} {
		if _, err := registry.Enroll(id, nil, remote.DeviceMetadata{}, []contracts.Capability{contracts.CapInteractive, contracts.CapApprover}); err != nil {
			t.Fatalf("enroll %s: %v", id, err)
		}
	}
	// dev-c is scoped to a DIFFERENT thread entirely — the U16
	// CheckThread handoff (remote/capability.go's HANDOFF NOTE: "U18's
	// attach path MUST call CheckThread").
	if _, err := registry.Enroll("dev-c", nil, remote.DeviceMetadata{}, []contracts.Capability{contracts.CapInteractive, contracts.CapApprover}); err != nil {
		t.Fatalf("enroll dev-c: %v", err)
	}
	if err := registry.SetConstraints("dev-c", remote.DeviceConstraints{AllowedThreads: []string{"th_somewhere_else"}}); err != nil {
		t.Fatalf("constrain dev-c: %v", err)
	}

	var d *daemon.Daemon
	var resolveCalls int64
	factory := func(threadID string, meta contracts.ThreadMeta) agoraio.Engine {
		return newSessionApprovalEngine(func() *daemon.Daemon { return d }, threadID, "tu_0001", &resolveCalls)
	}
	d = daemon.NewDaemon(ctx, daemon.Config{Registry: registry, EngineFactory: factory})
	defer d.Close()

	threadID, err := d.CreateThread(contracts.ThreadMeta{ThreadID: "th_0002fanout", Profile: "dev"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	sockPath := sessionSockPath(t, "approval-fanout.sock")
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

	if err := backendA.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "go"}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}
	waitForType(t, backendA, contracts.EvApprovalRequested)
	waitForType(t, backendB, contracts.EvApprovalRequested)

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

	resA := waitForFullResolution(t, backendA, "ap_0001")
	resB := waitForFullResolution(t, backendB, "ap_0001")
	if resA != resB {
		t.Fatalf("both clients must see the SAME resolution: A=%+v B=%+v", resA, resB)
	}
	wantByDecision := map[string]contracts.Decision{"dev-a": contracts.DecisionAllow, "dev-b": contracts.DecisionDeny}
	want, ok := wantByDecision[resA.By]
	if !ok || resA.Decision != want {
		t.Fatalf("resolution %+v not attributed to a real racer with its own decision", resA)
	}
	if got := atomic.LoadInt64(&resolveCalls); got != 1 {
		t.Fatalf("Resolve fired %d times, want exactly 1 (the real seam call happens once per id)", got)
	}

	// dev-c: thread-scoped to a different thread — attach must be refused.
	// Refusal is observable as: the connection closes without ever
	// delivering an event (ServeConn's authenticate() error return closes
	// the connection before ever calling Session.Attach — serve.go).
	backendC, err := tui.DialUnixBackend(sockPath, agoraio.AttachRequest{ThreadID: threadID, ClientID: "dev-c", Kind: "tui"})
	if err != nil {
		t.Fatalf("dial C: %v", err)
	}
	defer backendC.Close()
	select {
	case ev, ok := <-backendC.Events():
		if ok {
			t.Fatalf("dev-c (out-of-scope thread) received an event, want the attach refused: %+v", ev)
		}
		// channel closed: the server refused and closed the connection.
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the out-of-scope device's connection to be refused/closed")
	}
}

func sessionSockPath(t *testing.T, name string) string {
	t.Helper()
	base := ""
	if runtime.GOOS == "darwin" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "agconform")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

func waitForType(t *testing.T, b tui.Backend, want contracts.EventType) contracts.Event {
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

// waitForFullResolution scans for the FULL approval.resolved (Stage set) —
// ignoring io.Session's own thin {id,by} broadcast (Stage empty, §6
// resolution 2).
func waitForFullResolution(t *testing.T, b tui.Backend, id string) contracts.ApprovalResolution {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-b.Events():
			if ev.Type != contracts.EvApprovalResolved {
				continue
			}
			res := approvalResolvedFields(t, ev)
			if res.ID != id || res.Stage == "" {
				continue
			}
			return res
		case <-deadline:
			t.Fatalf("timed out waiting for the full approval.resolved for %s", id)
		}
	}
}
