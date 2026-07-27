// security_test.go: the U18 review-gate fixes (NEX-765) — nil-Registry
// fail-closed default (finding #1), remote.CheckApproval wiring on the
// approval-resolution path (finding #2), and the ServeConn read-loop
// goroutine leak on ctx cancel (finding #4). Each test proves the FAILING
// behavior would be caught before the fix (TDD-first per the fix brief).
package daemon

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
)

// --- finding #1: nil-Registry fail-OPEN -> fail-closed by default ---

// TestAuthenticate_NilRegistry_FailsClosedByDefault proves the shipped
// (cmd/agora's runDaemon) default: with no Registry configured and no
// explicit InsecureTrustWireCaps opt-in, a client self-declaring
// CapAdmin/CapApprover on the wire is granted CapObserver ONLY — never the
// capabilities it claimed. Before the fix, authenticate() trusted
// req.Capabilities verbatim here (fail-OPEN: any client could self-declare
// CapAdmin/CapApprover and walk through the approval gate).
func TestAuthenticate_NilRegistry_FailsClosedByDefault(t *testing.T) {
	d := NewDaemon(context.Background(), Config{EngineFactory: constFactory(&agoraio.ScriptedEngine{})})

	info, err := d.authenticate(agoraio.AttachRequest{
		ClientID:     "attacker",
		Kind:         "tui",
		Capabilities: []contracts.Capability{contracts.CapAdmin, contracts.CapApprover, contracts.CapInteractive},
	}, false)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if len(info.Capabilities) != 1 || info.Capabilities[0] != contracts.CapObserver {
		t.Fatalf("nil-Registry, no opt-in: got capabilities %v, want exactly [CapObserver] (fail closed, ignoring the self-declared caps)", info.Capabilities)
	}
	if info.ClientID != "attacker" {
		t.Fatalf("ClientID = %q, want passthrough %q", info.ClientID, "attacker")
	}
}

// TestAuthenticate_NilRegistry_InsecureOptIn_TrustsWireCaps proves the dev
// escape hatch: with Config.InsecureTrustWireCaps explicitly set true, the
// old wire-trust behavior is preserved verbatim (an explicit, documented
// choice — not the default).
func TestAuthenticate_NilRegistry_InsecureOptIn_TrustsWireCaps(t *testing.T) {
	d := NewDaemon(context.Background(), Config{
		EngineFactory:         constFactory(&agoraio.ScriptedEngine{}),
		InsecureTrustWireCaps: true,
	})

	want := []contracts.Capability{contracts.CapAdmin, contracts.CapApprover}
	info, err := d.authenticate(agoraio.AttachRequest{ClientID: "dev-box", Kind: "tui", Capabilities: want}, false)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if len(info.Capabilities) != len(want) || info.Capabilities[0] != want[0] || info.Capabilities[1] != want[1] {
		t.Fatalf("insecure opt-in: got capabilities %v, want the wire-declared %v", info.Capabilities, want)
	}
}

// TestServeConn_NilRegistry_SelfDeclaredCapApprover_CannotResolveApproval is
// the end-to-end wire proof: an attacker dials the shipped nil-Registry
// (no opt-in) daemon, self-declares CapApprover on attach, and tries to
// resolve a live approval. Before the fix, this genuinely succeeded (the
// exact exploit finding #1 describes). After the fix, io.Session's own
// capability gate (handleInput: contracts.Holds(from.info.Capabilities,
// RequiredForInput(...))) refuses the CapObserver-only attacker, the
// connection is torn down, and the approval is never resolved.
func TestServeConn_NilRegistry_SelfDeclaredCapApprover_CannotResolveApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var d *Daemon
	factory := func(threadID string, meta contracts.ThreadMeta) agoraio.Engine {
		return &immediateApprovalEngine{daemon: func() *Daemon { return d }, threadID: threadID}
	}
	// No Registry, no InsecureTrustWireCaps — the exact shipped runDaemon
	// wiring (cmd/agora/daemon.go: daemon.NewDaemon(ctx, daemon.Config{})).
	d = NewDaemon(ctx, Config{EngineFactory: factory})
	defer d.Close()

	threadID, err := d.CreateThread(contracts.ThreadMeta{ThreadID: "th_attack", Profile: "dev"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	sockPath := shortSockPath(t, "attacker.sock")
	ln, err := agoraio.ListenUnix(sockPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	// ServeConn, NOT ServeUnix: this test's threat model is a REMOTE client
	// (the ws lane), and ServeUnix now grants the local owner interactive
	// capability because the 0700 socket already restricts it to the
	// daemon's own uid (agora#133). The unix socket here is just a
	// convenient pipe; the lane under test is the remote one, which still
	// fails closed to CapObserver.
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() { _ = d.ServeConn(ctx, conn) }()
		}
	}()

	attacker, err := tui.DialUnixBackend(sockPath, agoraio.AttachRequest{
		ThreadID:     threadID,
		ClientID:     "attacker",
		Kind:         "tui",
		Capabilities: []contracts.Capability{contracts.CapAdmin, contracts.CapApprover, contracts.CapInteractive},
		Replay:       16,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer attacker.Close()

	// immediateApprovalEngine raises approval.requested as soon as the
	// session mints (Attach, not a user_message) — attaching needs no
	// capability at all, only SENDING Input does, so the CapObserver-only
	// attacker can still see the request; it just can't legally resolve it.
	waitFor(t, attacker, contracts.EvApprovalRequested)

	// The attacker answers the approval it was only ever able to OBSERVE
	// (CapObserver), never legitimately authorized to resolve.
	if err := attacker.Send(ctx, contracts.Input{Type: contracts.InApprovalResponse, ID: "ap_0001", Decision: contracts.DecisionAllow}); err != nil {
		t.Fatalf("send approval_response (write itself never fails — it's fire-and-forget over the wire): %v", err)
	}

	// No genuine approval.resolved should ever arrive, and the connection
	// should be torn down by the server (ErrUnauthorized breaks ServeConn's
	// read loop) — same observable shape as the existing CheckThread refusal
	// test (flow_approval_session_test.go's dev-c case).
	select {
	case ev, ok := <-attacker.Events():
		if ok && ev.Type == contracts.EvApprovalResolved {
			var res contracts.ApprovalResolution
			if err := json.Unmarshal(ev.Payload, &res); err != nil {
				t.Fatalf("decode approval.resolved: %v", err)
			}
			if res.Stage != "" {
				t.Fatalf("attacker's forged approval_response produced a REAL resolution: %+v — fail-open bypass still live", res)
			}
		}
	case <-time.After(2 * time.Second):
		// no event at all within the window is also an acceptable refusal
		// shape (the connection may already be torn down).
	}
}

// immediateApprovalEngine raises approval.requested{ap_0001,kind=exec} the
// instant Run starts — no user_message needed to trigger it — so a
// CapObserver-only attacker (which cannot legally SEND any Input at all)
// can still witness the request and attempt (illegally) to resolve it.
type immediateApprovalEngine struct {
	daemon   func() *Daemon
	threadID string
}

func (e *immediateApprovalEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	select {
	case out <- contracts.Event{Type: contracts.EvApprovalRequested, ThreadID: e.threadID, Payload: mustMarshalJSON(contracts.ApprovalRequest{
		ID: "ap_0001", Kind: contracts.KindExec, Payload: map[string]string{"command": "go test"},
	})}:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case i, ok := <-in:
			if !ok {
				return nil
			}
			if i.Type != contracts.InApprovalResponse || i.ID != "ap_0001" {
				continue
			}
			by, err := e.daemon().WaitForBy(ctx, i.ID)
			if err != nil {
				return err
			}
			res := ResolveApproval(i.ID, contracts.KindExec, i, by)
			select {
			case out <- contracts.Event{Type: contracts.EvApprovalResolved, ThreadID: e.threadID, Payload: mustMarshalJSON(res)}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// --- finding #2: remote.CheckApproval never wired ---

// approvalScopedEngine raises TWO approvals of different kinds (exec then
// patch) and, on each's response, resolves via the real
// daemon.ResolveApproval + WaitForBy seam — mirroring awaitApprovalEngine
// but parameterized over kind so this test can prove a device scoped to
// AllowedApprovalKinds:[KindExec] is refused for the patch approval but
// allowed for the exec one.
type approvalScopedEngine struct {
	daemon   func() *Daemon
	threadID string
}

func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("security_test: marshal: " + err.Error())
	}
	return b
}

func (e *approvalScopedEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	raised := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case i, ok := <-in:
			if !ok {
				return nil
			}
			if !raised {
				raised = true
				reqs := []contracts.ApprovalRequest{
					{ID: "ap_exec", Kind: contracts.KindExec, Payload: map[string]string{"command": "go test"}},
					{ID: "ap_patch", Kind: contracts.KindPatch, Payload: map[string]string{"diff": "+1 -1"}},
				}
				for _, req := range reqs {
					select {
					case out <- contracts.Event{Type: contracts.EvApprovalRequested, ThreadID: e.threadID, Payload: mustMarshalJSON(req)}:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				continue
			}
			if i.Type != contracts.InApprovalResponse {
				continue
			}
			kind := contracts.KindExec
			if i.ID == "ap_patch" {
				kind = contracts.KindPatch
			}
			by, err := e.daemon().WaitForBy(ctx, i.ID)
			if err != nil {
				return err
			}
			res := ResolveApproval(i.ID, kind, i, by)
			select {
			case out <- contracts.Event{Type: contracts.EvApprovalResolved, ThreadID: e.threadID, Payload: mustMarshalJSON(res)}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// TestServeConn_DeviceScopedToApprovalKind_RefusedOutsideItsAllowedKinds is
// the wire-level proof for finding #2: a device holding CapApprover but
// constrained via AllowedApprovalKinds:[KindExec] answers a KindExec
// approval (allowed) and a KindPatch approval (refused) — before the fix,
// io.Session's own capability gate only checked the coarse CapApprover
// tier (RequiredForApproval collapses every non-question kind to
// CapApprover), so the patch response would have resolved too.
func TestServeConn_DeviceScopedToApprovalKind_RefusedOutsideItsAllowedKinds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := remote.NewRegistry(nil)
	if _, err := registry.Enroll("scoped-approver", nil, remote.DeviceMetadata{}, []contracts.Capability{contracts.CapInteractive, contracts.CapApprover}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := registry.SetConstraints("scoped-approver", remote.DeviceConstraints{AllowedApprovalKinds: []contracts.ApprovalKind{contracts.KindExec}}); err != nil {
		t.Fatalf("SetConstraints: %v", err)
	}

	var d *Daemon
	factory := func(threadID string, meta contracts.ThreadMeta) agoraio.Engine {
		return &approvalScopedEngine{daemon: func() *Daemon { return d }, threadID: threadID}
	}
	d = NewDaemon(ctx, Config{Registry: registry, EngineFactory: factory})
	defer d.Close()

	threadID, err := d.CreateThread(contracts.ThreadMeta{ThreadID: "th_scoped", Profile: "dev"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	sockPath := shortSockPath(t, "scoped.sock")
	ln, err := agoraio.ListenUnix(sockPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	go func() { _ = d.ServeUnix(ctx, ln) }()

	backend, err := tui.DialUnixBackend(sockPath, agoraio.AttachRequest{ThreadID: threadID, ClientID: "scoped-approver", Kind: "tui", Replay: 16})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer backend.Close()

	if err := backend.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "go"}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}
	waitFor(t, backend, contracts.EvApprovalRequested)
	waitFor(t, backend, contracts.EvApprovalRequested)

	// Refused: the device is scoped to exec-only, this is a patch approval.
	if err := backend.Send(ctx, contracts.Input{Type: contracts.InApprovalResponse, ID: "ap_patch", Decision: contracts.DecisionAllow}); err != nil {
		t.Fatalf("send patch approval_response: %v", err)
	}
	// Allowed: exec is within its AllowedApprovalKinds.
	if err := backend.Send(ctx, contracts.Input{Type: contracts.InApprovalResponse, ID: "ap_exec", Decision: contracts.DecisionAllow}); err != nil {
		t.Fatalf("send exec approval_response: %v", err)
	}

	// Drain for BOTH possible resolutions concurrently (never discarding
	// one while scanning for the other, unlike waitForResolution's
	// single-id scan — that would silently drop the very patch resolution
	// this test needs to inspect if it arrived first).
	resolutions := drainApprovalResolutions(backend, []string{"ap_exec", "ap_patch"}, 2*time.Second)
	if _, ok := resolutions["ap_exec"]; !ok {
		t.Fatal("exec approval never resolved — want it allowed (within the device's AllowedApprovalKinds)")
	}
	if res, ok := resolutions["ap_patch"]; ok {
		t.Fatalf("patch approval resolved despite the device's exec-only AllowedApprovalKinds constraint: %+v", res)
	}
}

// drainApprovalResolutions reads b's event stream until every id in ids has
// been seen as a FULL approval.resolved (Stage set — ignoring io.Session's
// own thin {id,by} broadcast) or timeout elapses, returning whichever
// resolutions actually arrived. Draining for every wanted id concurrently
// (rather than scanning for one id at a time, like waitForResolution)
// avoids silently discarding a resolution for a DIFFERENT id that arrives
// first — the exact shape this test needs to catch a false negative.
func drainApprovalResolutions(b tui.Backend, ids []string, timeout time.Duration) map[string]contracts.ApprovalResolution {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make(map[string]contracts.ApprovalResolution)
	deadline := time.After(timeout)
	for len(out) < len(want) {
		select {
		case ev, ok := <-b.Events():
			if !ok {
				return out
			}
			if ev.Type != contracts.EvApprovalResolved {
				continue
			}
			var res contracts.ApprovalResolution
			if err := json.Unmarshal(ev.Payload, &res); err != nil || res.Stage == "" {
				continue
			}
			if want[res.ID] {
				out[res.ID] = res
			}
		case <-deadline:
			return out
		}
	}
	return out
}

// --- finding #4: ServeConn read-loop goroutine/FD leak on ctx cancel ---

// TestServeConn_ExitsOnCtxCancel_EvenWithIdleClient proves ServeConn's read
// loop (blocked in bufio.Scanner.Scan on an idle-but-connected client) is
// unblocked when the daemon's context is canceled — before the fix, Scan()
// has no read deadline and no ctx.Done() escape, so it blocks forever and
// ServeConn (and its goroutine + the connection's FD) never returns/closes.
func TestServeConn_ExitsOnCtxCancel_EvenWithIdleClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	d := NewDaemon(ctx, Config{EngineFactory: constFactory(&agoraio.ScriptedEngine{})})
	defer d.Close()
	threadID, err := d.CreateThread(contracts.ThreadMeta{ThreadID: "th_idle", Profile: "dev"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	sockPath := shortSockPath(t, "idle.sock")
	ln, err := agoraio.ListenUnix(sockPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	go func() { _ = d.ServeUnix(ctx, ln) }()

	// A connected client that attaches and then goes silent (never sends
	// another frame, never closes) — the idle-but-connected shape the
	// finding describes.
	backend, err := tui.DialUnixBackend(sockPath, agoraio.AttachRequest{ThreadID: threadID, ClientID: "idle-client", Kind: "tui"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer backend.Close()

	// Give ServeConn a moment to actually attach before cancel — otherwise
	// this test could race the connection's own setup, which isn't the
	// property under test.
	time.Sleep(50 * time.Millisecond)

	cancel()

	// If ServeConn's read loop leaked (blocked in Scan() forever), the
	// connection's FD is never closed. We can observe THAT directly: the
	// client side's read loop (ioBackend.readLoop) only ever sees its
	// Events channel close when the server closes the connection (rw.Close
	// deferred in ServeConn) — which only fires once ServeConn returns.
	select {
	case _, ok := <-backend.Events():
		if ok {
			t.Fatal("unexpected event after ctx cancel with an idle client")
		}
		// channel closed: readLoop exited because the server closed the
		// connection, i.e. ServeConn actually returned (no leak).
	case <-time.After(3 * time.Second):
		t.Fatal("server never closed the idle connection after ctx cancel — ServeConn's read loop leaked (finding #4)")
	}
}

// TestAuthenticate_LocalOwner_GetsInteractiveAndApprover covers agora#133.
//
// The shipped `agora daemon` never configures a Registry, so every client
// fell to CapObserver and Session.handleInput refused every user message —
// the lane could not serve a turn to anyone, including the operator who
// started it. A connection arriving on the unix socket is already
// restricted to that operator's uid (io.ListenUnix chmods it 0700, so the
// kernel refuses any other user's connect), which is the same trust
// boundary the in-process lane already runs at.
func TestAuthenticate_LocalOwner_GetsInteractiveAndApprover(t *testing.T) {
	d := NewDaemon(context.Background(), Config{EngineFactory: constFactory(&agoraio.ScriptedEngine{})})

	info, err := d.authenticate(agoraio.AttachRequest{ClientID: "operator-tui", Kind: "tui"}, true)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	has := func(c contracts.Capability) bool {
		for _, got := range info.Capabilities {
			if got == c {
				return true
			}
		}
		return false
	}
	for _, want := range []contracts.Capability{contracts.CapObserver, contracts.CapInteractive, contracts.CapApprover} {
		if !has(want) {
			t.Errorf("local owner missing %s (got %v) — the daemon lane cannot serve a turn without it (agora#133)", want, info.Capabilities)
		}
	}
	// CapAdmin stays behind a real registry identity: "is the local uid" is
	// a weaker claim than it looks once anything else runs as that user.
	if has(contracts.CapAdmin) {
		t.Errorf("local owner was granted CapAdmin (%v); want admin to require a registry identity", info.Capabilities)
	}
}

// TestAuthenticate_RemoteStillFailsClosed guards the other direction: the
// local-owner grant must key on the TRANSPORT, not leak to the ws lane,
// which has no uid restriction behind it.
func TestAuthenticate_RemoteStillFailsClosed(t *testing.T) {
	d := NewDaemon(context.Background(), Config{EngineFactory: constFactory(&agoraio.ScriptedEngine{})})

	info, err := d.authenticate(agoraio.AttachRequest{
		ClientID:     "attacker",
		Kind:         "tui",
		Capabilities: []contracts.Capability{contracts.CapAdmin, contracts.CapApprover, contracts.CapInteractive},
	}, false)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if len(info.Capabilities) != 1 || info.Capabilities[0] != contracts.CapObserver {
		t.Fatalf("remote (ws) with nil Registry: got %v, want exactly [CapObserver] — agora#133's local grant must not reach this lane", info.Capabilities)
	}
}

// TestServeUnix_LocalOwnerCanActuallySendATurn is the test agora#133 asked
// for: build the daemon the way cmd/agora's runDaemon does — no Registry,
// no InsecureTrustWireCaps — attach over the unix socket, and assert a user
// message is ACCEPTED.
//
// The suite could not previously catch this. Every conformance drive that
// exercises capability enforcement configures a real *remote.Registry, so
// the POLICY was tested thoroughly and the CONFIGURATION the binary
// actually produces was never tested at all — which is how a daemon that
// could not serve a turn to anyone shipped.
func TestServeUnix_LocalOwnerCanActuallySendATurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	seen := make(chan contracts.Input, 4)
	factory := func(threadID string, meta contracts.ThreadMeta) agoraio.Engine {
		return &recordingEngine{threadID: threadID, seen: seen}
	}
	// The exact shipped runDaemon wiring.
	d := NewDaemon(ctx, Config{EngineFactory: factory})
	defer d.Close()

	threadID, err := d.CreateThread(contracts.ThreadMeta{ThreadID: "th_local", Profile: "dev"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	sockPath := shortSockPath(t, "local.sock")
	ln, err := agoraio.ListenUnix(sockPath)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	go func() { _ = d.ServeUnix(ctx, ln) }()

	// Exactly what cmd/agora's dialBackend requests.
	client, err := tui.DialUnixBackend(sockPath, agoraio.AttachRequest{
		ThreadID:     threadID,
		ClientID:     "operator-tui",
		Kind:         "tui",
		Capabilities: []contracts.Capability{contracts.CapInteractive, contracts.CapApprover},
		Replay:       16,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "hello"}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}

	select {
	case got := <-seen:
		if got.Type != contracts.InUserMessage || got.Text != "hello" {
			t.Fatalf("engine saw %+v; want the user_message", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the engine never received the user message — the daemon lane rejected it on capability grounds, which is agora#133: a shipped `agora daemon` that cannot serve a turn to anyone, including the operator who started it")
	}
}

// recordingEngine forwards every Input it receives to seen.
type recordingEngine struct {
	threadID string
	seen     chan<- contracts.Input
}

func (e *recordingEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case i, ok := <-in:
			if !ok {
				return nil
			}
			select {
			case e.seen <- i:
			default:
			}
		}
	}
}
