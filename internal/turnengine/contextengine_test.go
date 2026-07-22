package turnengine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// --- ctxmap wiring (U-D1) ---

// recallCall builds a bridle.ToolInvocation for ctxmap's recall tool — one
// of the tools memory.Engine.Tools() adds to the request via ctxmap's own
// BeforeModelCall hook (bridleadapter.Attach), never one of this Manager's
// OWN Surface tools.
func recallCall(id, query string) bridle.ToolInvocation {
	args, _ := json.Marshal(map[string]string{"query": query})
	return bridle.ToolInvocation{ID: id, Name: "recall", Args: args}
}

// TestManager_ContextEngine_WorkingStateInSystemPrompt drives a turn with
// one host-tool call (write_file, auto-allowed) and asserts the NEXT
// request follows NEX-793's cache-aware placement: AppendSystemPrompt
// carries DevProfile's own harness note AND ctxmap's framing (the static
// prefix — ctxmap must not clobber the host note), while the CHURNING
// working-state block ("your own progress so far", memory.go's
// renderWorkingState) rides as the LAST message — never in the system
// prompt, where its per-tool-call mutation busted the provider prefix
// cache from position 0.
func TestManager_ContextEngine_WorkingStateInSystemPrompt(t *testing.T) {
	roots := managerTestRoots(t)
	policy := defaultPolicy()
	policy[contracts.KindPatch] = contracts.PolicyAuto
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "hello")}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_ctxmap_ws", provider, WithRoots(roots), WithPolicy(policy), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))
	if m.eng == nil {
		t.Fatal("NewManager: context engine did not construct (m.eng nil) — expected default-ON")
	}

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

	sys := provider.LastRequest().AppendSystemPrompt
	if !strings.Contains(sys, devSystemPrompt) {
		t.Fatalf("AppendSystemPrompt lost DevProfile's own note (composition broke); got:\n%s", sys)
	}
	if !strings.Contains(sys, "Working memory (automatic)") {
		t.Fatalf("AppendSystemPrompt missing ctxmap's framing; got:\n%s", sys)
	}
	// NEX-793: the churning block must NOT be in the system prompt (that
	// placement rewrote position 0 on every tool call → zero cache hits) …
	if strings.Contains(sys, "Working state") {
		t.Fatalf("working-state block leaked back into the system prompt (cache-busting placement); got:\n%s", sys)
	}
	// … it rides as the LAST message instead, carrying this turn's progress.
	msgs := provider.LastRequest().Messages
	if len(msgs) == 0 {
		t.Fatal("no messages in the last request")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Working state") {
		t.Fatalf("working-state block not in the final message; got: %+v", last)
	}
	if !strings.Contains(last.Content, "note.txt") {
		t.Fatalf("working-state block does not mention the file this turn wrote (note.txt); got:\n%s", last.Content)
	}
}

// TestManager_ContextEngine_RecallNotGated scripts a recall tool call (one
// of ctxmap's own tools, never one of this Manager's Surface tools) and
// asserts: (1) NO EvApprovalRequested is ever emitted for it — the U-D1
// approval-hook passthrough — and (2) the tool_result actually comes back
// from ctxmap (served by its own BeforeToolCall hook via Deny+Result), not
// an "unknown tool" error from surfaceRunner.
func TestManager_ContextEngine_RecallNotGated(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{recallCall("1", "anything")}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_ctxmap_recall", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))
	if m.eng == nil {
		t.Fatal("NewManager: context engine did not construct (m.eng nil)")
	}

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "recall something"}

	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; want turn.completed (and no approval.requested for recall)", got)
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	toolMsg := lastToolResultMessage(t, provider.LastRequest())
	if strings.Contains(toolMsg.Content, "unknown tool") || strings.Contains(toolMsg.Content, "ErrUnknownTool") {
		t.Fatalf("recall tool_result looks like a surfaceRunner unknown-tool error, not ctxmap's own serve: %q", toolMsg.Content)
	}
}

// TestManager_ContextEngine_SurfaceToolsStillGated is the regression guard
// (per the brief): the foreign-tool passthrough added to approval.go's
// beforeToolCall (U-D1) must NOT un-gate agora's own Surface tools —
// write_file and run_command still ask under DevProfile's default policy.
func TestManager_ContextEngine_SurfaceToolsStillGated(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "should ask")}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_ctxmap_gate_write", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))
	if m.eng == nil {
		t.Fatal("NewManager: context engine did not construct (m.eng nil)")
	}

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
	if ar.Kind != contracts.KindPatch {
		t.Fatalf("approval kind = %q; want patch", ar.Kind)
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
}

// TestManager_ContextEngine_RunCommandStillGated: run_command (KindExec)
// still asks too — the passthrough check is keyed on m.surface.Handles,
// not on a specific tool name, so this covers a second Surface family.
func TestManager_ContextEngine_RunCommandStillGated(t *testing.T) {
	roots := managerTestRoots(t)
	args, _ := json.Marshal(map[string]string{"command": "true"})
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{{ID: "1", Name: toolrunner.ToolRunCommand, Args: args}}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_ctxmap_gate_exec", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "run true"}

	req := recvApprovalRequested(t, out, testTimeout)
	var ar contracts.ApprovalRequest
	if err := json.Unmarshal(req.Payload, &ar); err != nil {
		t.Fatalf("decode approval.requested payload: %v", err)
	}
	if ar.Kind != contracts.KindExec {
		t.Fatalf("approval kind = %q; want exec", ar.Kind)
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
}

// TestManager_ContextEngine_ReadFileStillAutoAllowed: read_file (KindRead)
// still auto-allows under defaultPolicy — the passthrough check runs
// BEFORE classification, so it must not interfere with an already-gated-
// but-auto-allowed kind either.
func TestManager_ContextEngine_ReadFileStillAutoAllowed(t *testing.T) {
	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: toolrunner.ToolReadFile, Args: json.RawMessage(`{"path":"hello.txt"}`)},
		}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_ctxmap_read_auto", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "read hello.txt"}

	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; want turn.completed", got)
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}

// TestManager_ContextEngine_WithContextEngineFalseDisables asserts the
// escape hatch: WithContextEngine(false) leaves m.eng/m.detach nil and a
// turn still runs to completion with plain AppendSystemPrompt (no
// working-state block) — the degrade-without-ctxmap posture, forced
// explicitly rather than by a construction failure.
func TestManager_ContextEngine_WithContextEngineFalseDisables(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "hello"})
	m := NewManager("th_ctxmap_off", provider, WithContextEngine(false), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))
	if m.eng != nil {
		t.Fatal("WithContextEngine(false): m.eng should be nil")
	}
	if m.detach != nil {
		t.Fatal("WithContextEngine(false): m.detach should be nil")
	}

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 8)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	sys := provider.LastRequest().AppendSystemPrompt
	if strings.Contains(sys, "Working state") {
		t.Fatalf("WithContextEngine(false): AppendSystemPrompt still carries a working-state block; got:\n%s", sys)
	}
	// Prompt-assembly unit: AppendSystemPrompt is now the FULL composed
	// prompt (core contract + devSystemPrompt's profile block + environment,
	// see profile.go's composeDevSystemPrompt), not just the bare
	// devSystemPrompt note — so the "no ctxmap addition" expectation
	// compares against a freshly-composed DevProfile() rather than the
	// literal devSystemPrompt constant. Two independently-composed calls in
	// the same test run are byte-identical (same wd/date/model), so this
	// still pins "WithContextEngine(false) added nothing".
	dev := DevProfile()
	if sys != dev.AppendSystemPrompt {
		t.Fatalf("AppendSystemPrompt = %q; want exactly DevProfile's own composed prompt (no ctxmap addition):\n%s", sys, dev.AppendSystemPrompt)
	}
}

// TestSurface_Handles exercises the toolrunner.Surface.Handles helper this
// unit adds directly (U-D1): fs/exec names handled, an unregistered/
// foreign name (ctxmap's recall) not.
func TestSurface_Handles(t *testing.T) {
	roots := managerTestRoots(t)
	s := toolrunner.NewSurface(nil, toolrunner.NewFSFamily(roots), toolrunner.NewExecFamily(roots))
	for _, name := range []string{toolrunner.ToolReadFile, toolrunner.ToolWriteFile, toolrunner.ToolRunCommand} {
		if !s.Handles(name) {
			t.Errorf("Handles(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"recall", "inspect", "read_raw", "does_not_exist"} {
		if s.Handles(name) {
			t.Errorf("Handles(%q) = true, want false (foreign tool)", name)
		}
	}
}

// recordingSubprocessProvider wraps fake.SubprocessProvider to capture the
// lowered ProviderRequest — the fake discards it, and the shadowing
// regression below is entirely about what the request carries.
type recordingSubprocessProvider struct {
	*fake.SubprocessProvider
	lastReq bridle.ProviderRequest
}

func (p *recordingSubprocessProvider) RunTurn(ctx context.Context, req bridle.ProviderRequest, sink bridle.EventSink) (bridle.ProviderResult, error) {
	p.lastReq = req
	return p.SubprocessProvider.RunTurn(ctx, req, sink)
}

// TestManager_SubprocessDefault_NoCtxmapInjection: the live-turn shadowing
// regression (2026-07-21). A subprocess-category default provider (the
// claudesdk lane) lowers Messages via LastUserPrompt — so ctxmap's
// trailing working-memory user message SHADOWED the operator's real text,
// and every turn arrived at the model as just the empty memory scaffold
// ("standing by, no task received"). The Manager must NOT attach ctxmap
// injection to a subprocess default harness: the engine still constructs
// (alt direct-api harnesses share it), but the subprocess request carries
// no working-memory message and the operator's text is the LAST user
// message the provider sees.
func TestManager_SubprocessDefault_NoCtxmapInjection(t *testing.T) {
	provider := &recordingSubprocessProvider{SubprocessProvider: fake.NewSubprocessProvider(
		fake.SubprocessStep{Text: "ack", StopReason: bridle.StopReasonModelDone},
		fake.SubprocessStep{Text: "ack2", StopReason: bridle.StopReasonModelDone},
	)}
	m, in, out, runErr := newTestManagerWithStore(t, "th_subproc_ctx", nil, provider)

	if m.eng == nil {
		t.Fatal("context engine did not construct — it must still exist for alt direct-api harnesses")
	}
	if m.detach != nil {
		t.Fatal("ctxmap injection attached to a subprocess default harness — trailing memory message would shadow the operator's prompt")
	}

	runTurnAndDrain(t, m, in, out, runErr, "first turn")
	runTurnAndDrain(t, m, in, out, runErr, "read the docs/spec")

	msgs := provider.lastReq.Messages
	if len(msgs) == 0 {
		t.Fatal("no messages lowered to the subprocess provider")
	}
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "## Working memory") {
			t.Fatalf("working-memory block leaked into a subprocess request message: %q", msg.Content)
		}
	}
	if strings.Contains(provider.lastReq.AppendSystemPrompt, "## Working memory") {
		t.Fatal("working-memory block leaked into the subprocess AppendSystemPrompt")
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" || last.Content != "read the docs/spec" {
		t.Fatalf("last lowered message = %+v; want the operator's text as the final user message (LastUserPrompt takes the trailing run)", last)
	}
	endAndClose(t, in, out, runErr)
}
