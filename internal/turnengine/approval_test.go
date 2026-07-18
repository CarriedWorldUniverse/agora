package turnengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// allowAllPolicy is a test-only PolicySet that auto-allows every known
// kind — used by pre-U-C3 tests (manager_test.go/exec_unix_test.go's tool-
// DISPATCH tests, written back when tool execution was UNGATED) so they
// keep exercising surfaceRunner dispatch/tool-family behavior without also
// being coupled to approval semantics, which now has its own dedicated
// coverage in this file. Without this, every one of those tests would hang
// forever on an unanswered EvApprovalRequested the instant the
// BeforeToolCall hook this unit adds started gating every tool call —
// including read_file/does_not_exist, which toolrunner.Classify's switch
// has no dedicated case for and so falls through to its `default:` branch
// (KindEscalation, "unrecognized tool call: ...") — Classify's own
// behavior, unmodified and out of this unit's scope to change.
func allowAllPolicy() contracts.PolicySet {
	return contracts.PolicySet{
		contracts.KindExec:       contracts.PolicyAuto,
		contracts.KindPatch:      contracts.PolicyAuto,
		contracts.KindEscalation: contracts.PolicyAuto,
		contracts.KindMCPTool:    contracts.PolicyAuto,
		contracts.KindQuestion:   contracts.PolicyPrompt, // PolicyAuto is invalid for question (approval.Decide fail-closes it to ask regardless); left prompt for honesty, unused by any fs/exec test.
		contracts.KindPlan:       contracts.PolicyAuto,
	}
}

// writeFileCall builds a bridle.ToolInvocation for write_file — classifies
// as contracts.KindPatch, no shell involved (cross-platform, unlike
// run_command/KindExec which shells out via /bin/sh — see exec.go — and
// would need a //go:build !windows split per this repo's standing rule).
func writeFileCall(id, path, content string) bridle.ToolInvocation {
	args, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return bridle.ToolInvocation{ID: id, Name: toolrunner.ToolWriteFile, Args: args}
}

// recvApprovalRequested drains ch until it observes an approval.requested
// event, failing the test if the turn ends or the deadline elapses first
// (a missing ask when one was expected must not just hang forever).
func recvApprovalRequested(t *testing.T, ch <-chan contracts.Event, d time.Duration) contracts.Event {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("out closed before an approval.requested event arrived")
			}
			if ev.Type == contracts.EvApprovalRequested {
				return ev
			}
			if ev.Type == contracts.EvTurnFailed || ev.Type == contracts.EvTurnCompleted {
				t.Fatalf("turn ended (%s) before an approval.requested event arrived", ev.Type)
			}
		case <-deadline:
			t.Fatal("timed out waiting for approval.requested")
		}
	}
}

// drainNoApprovalRequestedToTurnEnd drains ch to a terminal turn event,
// asserting NO approval.requested was ever seen along the way — used by
// the auto-allow and scope-short-circuit tests to prove the ask never
// fired.
func drainNoApprovalRequestedToTurnEnd(t *testing.T, ch <-chan contracts.Event, d time.Duration) contracts.EventType {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("out closed before the turn ended")
			}
			if ev.Type == contracts.EvApprovalRequested {
				t.Fatal("unexpected approval.requested — this call should have been auto-decided, never asked")
			}
			if ev.Type == contracts.EvTurnFailed || ev.Type == contracts.EvTurnCompleted {
				return ev.Type
			}
		case <-deadline:
			t.Fatal("timed out waiting for the turn to end")
		}
	}
}

// --- Ask -> approve -> executes ---

func TestManager_Approval_AskApproveExecutes(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "hello from the model")}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_ask_approve", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}

	req := recvApprovalRequested(t, out, testTimeout)
	var ar contracts.ApprovalRequest
	if err := json.Unmarshal(req.Payload, &ar); err != nil {
		t.Fatalf("decode approval.requested payload: %v", err)
	}
	if ar.ID != "1" {
		t.Fatalf("approval id = %q; want tool call id %q", ar.ID, "1")
	}
	if ar.Kind != contracts.KindPatch {
		t.Fatalf("approval kind = %q; want patch", ar.Kind)
	}
	var pp toolrunner.PatchPayload
	if pb, err := json.Marshal(ar.Payload); err == nil {
		_ = json.Unmarshal(pb, &pp)
	}
	if pp.Path != "note.txt" {
		t.Fatalf("approval payload path = %q; want note.txt (payload=%+v)", pp.Path, ar.Payload)
	}

	in <- contracts.Input{Type: contracts.InApprovalResponse, ID: ar.ID, Decision: contracts.DecisionAllow, Scope: contracts.ScopeOnce}

	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed after approve")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	got, err := os.ReadFile(filepath.Join(roots.WorkingDir, "note.txt"))
	if err != nil {
		t.Fatalf("read note.txt: %v", err)
	}
	if string(got) != "hello from the model" {
		t.Fatalf("note.txt content = %q; want %q (the tool must have actually executed)", got, "hello from the model")
	}
}

// --- Ask -> deny -> denied tool_result (turn still completes) ---

func TestManager_Approval_AskDenyIsToolResultNotAbort(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "should never land on disk")}},
		fake.Step{Text: "ok, I won't"},
	)
	m := NewManager("th_ask_deny", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}

	req := recvApprovalRequested(t, out, testTimeout)
	var ar contracts.ApprovalRequest
	if err := json.Unmarshal(req.Payload, &ar); err != nil {
		t.Fatalf("decode approval.requested payload: %v", err)
	}

	in <- contracts.Input{Type: contracts.InApprovalResponse, ID: ar.ID, Decision: contracts.DecisionDeny, Message: "not right now"}

	// Assert the turn completes NORMALLY (turn.completed), not turn.failed —
	// a deny is model-facing feedback, not a turn-abort (bridle's own
	// BeforeToolCallCtx.Deny doc comment; contrast HookAbort).
	var sawFailed, sawCompleted bool
	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev := <-out:
			switch ev.Type {
			case contracts.EvTurnFailed:
				sawFailed = true
				break loop
			case contracts.EvTurnCompleted:
				sawCompleted = true
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for the turn to end")
		}
	}
	if sawFailed || !sawCompleted {
		t.Fatalf("turn ended abnormally (failed=%v completed=%v); want turn.completed — a denial is feedback, not an abort", sawFailed, sawCompleted)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	if _, err := os.Stat(filepath.Join(roots.WorkingDir, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("note.txt should not exist on disk after a denial, stat err = %v", err)
	}

	toolMsg := lastToolResultMessage(t, provider.LastRequest())
	if toolMsg.Content == "" {
		t.Fatal("tool_result content empty; want the denial feedback")
	}
}

// --- Policy auto-allow: no ask, tool just runs ---

func TestManager_Approval_PolicyAutoAllowSkipsAsk(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "auto allowed")}},
		fake.Step{Text: "done"},
	)
	policy := defaultPolicy()
	policy[contracts.KindPatch] = contracts.PolicyAuto
	m := NewManager("th_auto", provider, WithRoots(roots), WithPolicy(policy), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}

	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; want turn.completed", got)
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	got, err := os.ReadFile(filepath.Join(roots.WorkingDir, "note.txt"))
	if err != nil {
		t.Fatalf("read note.txt: %v", err)
	}
	if string(got) != "auto allowed" {
		t.Fatalf("note.txt content = %q; want %q", got, "auto allowed")
	}
}

// --- Scope=session short-circuits a SECOND matching call in the same turn ---

func TestManager_Approval_ScopeSessionShortCircuitsSecondCall(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			writeFileCall("1", "one.txt", "first"),
			writeFileCall("2", "two.txt", "second"),
		}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_scope", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write both files"}

	// First call: ask, approve with Scope=session.
	req := recvApprovalRequested(t, out, testTimeout)
	var ar contracts.ApprovalRequest
	if err := json.Unmarshal(req.Payload, &ar); err != nil {
		t.Fatalf("decode approval.requested payload: %v", err)
	}
	if ar.ID != "1" {
		t.Fatalf("first approval id = %q; want 1", ar.ID)
	}
	in <- contracts.Input{Type: contracts.InApprovalResponse, ID: ar.ID, Decision: contracts.DecisionAllow, Scope: contracts.ScopeSession}

	// Second call (id "2", same session, same kind): must NOT ask again —
	// the scope grant recorded above short-circuits it straight to allow.
	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; want turn.completed", got)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	for name, want := range map[string]string{"one.txt": "first", "two.txt": "second"} {
		got, err := os.ReadFile(filepath.Join(roots.WorkingDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s content = %q; want %q", name, got, want)
		}
	}
}

// --- Interrupt during a pending approval: no deadlock ---

// TestManager_Approval_InterruptDuringPendingApproval blocks on the ask
// rendezvous (never sends an approval_response) and interrupts instead: the
// hook must unblock via turnCtx.Done() (askAndWait's second select), the
// turn must fail interrupted, out must close, and Run must return — all
// within testTimeout, with no goroutine left hanging. Run with
// `-race -count=10` (per the brief) to give the scheduler a real chance at
// the losing order across repeated whole-test-binary runs.
func TestManager_Approval_InterruptDuringPendingApproval(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "never written")}},
		fake.Step{Text: "unreachable"},
	)
	m := NewManager("th_interrupt", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}

	recvApprovalRequested(t, out, testTimeout)

	in <- contracts.Input{Type: contracts.InInterrupt}

	var sawFailed bool
	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				t.Fatal("out closed before turn.failed{interrupted:true} was observed")
			}
			if ev.Type == contracts.EvTurnFailed {
				var p turnFailedPayload
				if err := json.Unmarshal(ev.Payload, &p); err != nil {
					t.Fatalf("decode turn.failed payload: %v", err)
				}
				if !p.Interrupted {
					t.Fatalf("turn.failed payload = %+v; want interrupted:true", p)
				}
				sawFailed = true
				break loop
			}
		case <-deadline:
			t.Fatal("timed out draining out after interrupt — possible deadlock")
		}
	}
	if !sawFailed {
		t.Fatal("never observed turn.failed{interrupted:true}")
	}

	in <- contracts.Input{Type: contracts.InEnd}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned %v; want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Run did not return after end — possible deadlock")
	}

	if _, err := os.Stat(filepath.Join(roots.WorkingDir, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("note.txt should not exist on disk after interrupt, stat err = %v", err)
	}
}

// --- scopeKeyFor / recordScopeGrant unit coverage (pure, no engine) ---

func TestScopeKeyFor(t *testing.T) {
	cases := []struct {
		name    string
		kind    contracts.ApprovalKind
		payload any
		want    string
	}{
		{"exec_prefix_is_first_word", contracts.KindExec, toolrunner.ExecPayload{Command: "git status --short"}, "git"},
		{"exec_empty_command", contracts.KindExec, toolrunner.ExecPayload{Command: ""}, ""},
		{"patch_has_no_scope_key", contracts.KindPatch, toolrunner.PatchPayload{Path: "x"}, ""},
		{"escalation_has_no_scope_key", contracts.KindEscalation, toolrunner.EscalationPayload{Detail: "x"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scopeKeyFor(c.kind, c.payload); got != c.want {
				t.Fatalf("scopeKeyFor(%v, %+v) = %q; want %q", c.kind, c.payload, got, c.want)
			}
		})
	}
}

func TestManager_RecordScopeGrant_OnceNeverPersisted(t *testing.T) {
	m := NewManager("th_grant", fake.NewProvider())
	m.recordScopeGrant(contracts.KindPatch, contracts.ScopeOnce, "")
	if _, ok := m.scopeStore.Match(contracts.KindPatch, "th_grant", ""); ok {
		t.Fatal("ScopeOnce must never be persisted")
	}
}

func TestManager_RecordScopeGrant_SessionKeysOnThreadID(t *testing.T) {
	m := NewManager("th_grant2", fake.NewProvider())
	m.recordScopeGrant(contracts.KindPatch, contracts.ScopeSession, "")
	allow, ok := m.scopeStore.Match(contracts.KindPatch, "th_grant2", "")
	if !ok {
		t.Fatal("expected a session-scoped grant to match on the thread id")
	}
	if allow.Scope != contracts.ScopeSession {
		t.Fatalf("matched allow scope = %v; want session", allow.Scope)
	}
}
