package turnengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/hooks"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// --- fixture plumbing ----------------------------------------------------

// hooksTestEnv isolates a test's discovered hooks: a fresh project dir
// (t.TempDir()'s .agora/hooks.json) and a fresh HOME (t.Setenv, auto-restored)
// so DiscoverHooks' user-layer probe (~/.agora/hooks.json,
// ~/.agora/hooks-state.json) never touches the real operator's home.
type hooksTestEnv struct {
	t          *testing.T
	projectDir string
	homeDir    string
}

func newHooksTestEnv(t *testing.T) *hooksTestEnv {
	t.Helper()
	env := &hooksTestEnv{
		t:          t,
		projectDir: t.TempDir(),
		homeDir:    t.TempDir(),
	}
	t.Setenv("HOME", env.homeDir)
	t.Setenv("USERPROFILE", env.homeDir) // windows equivalent, harmless on unix
	if err := os.MkdirAll(filepath.Join(env.projectDir, ".agora"), 0o700); err != nil {
		t.Fatalf("mkdir .agora: %v", err)
	}
	return env
}

// writeConfig writes cfg as the project layer's hooks.json.
func (e *hooksTestEnv) writeConfig(cfg hooks.Config) {
	e.t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		e.t.Fatalf("marshal hooks.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(e.projectDir, ".agora", "hooks.json"), b, 0o600); err != nil {
		e.t.Fatalf("write hooks.json: %v", err)
	}
}

// trustHandler pre-computes the SAME PositionalKey/ContentHash
// hooks.Registry.Load + Resolve would compute for a project-layer handler
// at the given event/group/handler position, and appends a trusted-state
// entry for it (see loadHookState/hookStateEntry in hookrunner.go — the
// on-disk sidecar this unit persists trust state in). Tests call this for
// every handler that should actually be allowed to run; a handler NEVER
// passed here stays Untrusted (trust.go's fail-closed default) and must
// not run — exactly the "untrusted hook does not run" fixture.
func (e *hooksTestEnv) trustHandler(state map[string]hookStateEntry, event hooks.EventName, matcher string, h hooks.Handler, group, idx int) {
	rh := hooks.RegisteredHandler{
		Source:       hooks.Source{Layer: hooks.LayerProject, Path: filepath.Join(e.projectDir, ".agora")},
		Event:        event,
		GroupIndex:   group,
		HandlerIndex: idx,
		Matcher:      matcher,
		Handler:      h.Normalize(),
	}
	state[rh.PositionalKey()] = hookStateEntry{Enabled: true, TrustedHash: rh.ContentHash()}
}

// writeState persists state as ~/.agora/hooks-state.json (the User-layer
// trust sidecar DiscoverHooks reads via loadHookState).
func (e *hooksTestEnv) writeState(state map[string]hookStateEntry) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Join(e.homeDir, ".agora"), 0o700); err != nil {
		e.t.Fatalf("mkdir ~/.agora: %v", err)
	}
	b, err := json.Marshal(state)
	if err != nil {
		e.t.Fatalf("marshal hooks-state.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(e.homeDir, ".agora", "hooks-state.json"), b, 0o600); err != nil {
		e.t.Fatalf("write hooks-state.json: %v", err)
	}
}

// discover runs DiscoverHooks against this env's project dir, failing the
// test on any warning (a fixture bug, not something under test).
func (e *hooksTestEnv) discover() *HookRunner {
	e.t.Helper()
	hr, warnings := DiscoverHooks(e.projectDir)
	for _, w := range warnings {
		e.t.Errorf("unexpected hooks warning: %s", w)
	}
	if hr == nil {
		e.t.Fatal("DiscoverHooks returned nil — expected a discovered project hooks.json")
	}
	return hr
}

// catCommand is a portable "write my stdin to outPath" command — every
// fixture handler in this file is built from this, so assertions can
// decode the EXACT stdin JSON the engine sent, per event's spec §2 shape.

// skipWithoutPOSIXTools skips fixture tests whose command handlers are
// POSIX-shell one-liners (`cat > file`) — Windows CI has no sh/cat for the
// hooks engine's command handler to run. The engine's own cross-platform
// dispatch coverage lives in internal/hooks; these are integration
// fixtures for the turnengine wiring, and the wiring itself is
// OS-independent Go.
func skipWithoutPOSIXTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-shell hook fixtures; wiring is covered on unix CI")
	}
}

func catCommand(outPath string) string {
	return fmt.Sprintf("cat > %q", outPath)
}

func readJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v (raw=%s)", path, err, b)
	}
}

func waitForFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s to appear", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- PreToolUse / PostToolUse fire, tool still executes -------------------

// TestHooks_PreAndPostToolUse_FireWithSpecShapeAndToolStillExecutes is the
// ticket's core positive fixture: a real command hook on PreToolUse AND
// PostToolUse, both writing their stdin to a file — asserts the tool_name/
// tool_input shape (§2.1) on Pre, the tool_response shape (§2.3) on Post,
// and that the write_file call actually landed on disk (hooks observing a
// call must never themselves block a policy-allowed call).
func TestHooks_PreAndPostToolUse_FireWithSpecShapeAndToolStillExecutes(t *testing.T) {
	skipWithoutPOSIXTools(t)
	env := newHooksTestEnv(t)
	preOut := filepath.Join(env.projectDir, "pre.stdin.json")
	postOut := filepath.Join(env.projectDir, "post.stdin.json")

	preHandler := hooks.Handler{Type: hooks.HandlerCommand, Command: catCommand(preOut)}
	postHandler := hooks.Handler{Type: hooks.HandlerCommand, Command: catCommand(postOut)}
	env.writeConfig(hooks.Config{Hooks: hooks.EventMap{
		hooks.EventPreToolUse:  {{Matcher: "*", Hooks: []hooks.Handler{preHandler}}},
		hooks.EventPostToolUse: {{Matcher: "*", Hooks: []hooks.Handler{postHandler}}},
	}})

	state := map[string]hookStateEntry{}
	env.trustHandler(state, hooks.EventPreToolUse, "*", preHandler, 0, 0)
	env.trustHandler(state, hooks.EventPostToolUse, "*", postHandler, 0, 0)
	env.writeState(state)

	hr := env.discover()

	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "hooked hello")}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_hooks_pre_post", provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()), WithHooks(hr),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	go func() { _ = m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)

	got, err := os.ReadFile(filepath.Join(roots.WorkingDir, "note.txt"))
	if err != nil || string(got) != "hooked hello" {
		t.Fatalf("write_file did not execute: content=%q err=%v", got, err)
	}

	var pre preToolUseInput
	readJSONFile(t, preOut, &pre)
	if pre.ToolName != "write_file" {
		t.Errorf("PreToolUse tool_name = %q; want write_file", pre.ToolName)
	}
	if pre.HookEventName != "PreToolUse" {
		t.Errorf("PreToolUse hook_event_name = %q", pre.HookEventName)
	}
	var toolInput map[string]any
	if err := json.Unmarshal(pre.ToolInput, &toolInput); err != nil || toolInput["path"] != "note.txt" {
		t.Errorf("PreToolUse tool_input = %s; want path=note.txt", pre.ToolInput)
	}

	var post postToolUseInput
	readJSONFile(t, postOut, &post)
	if post.ToolName != "write_file" {
		t.Errorf("PostToolUse tool_name = %q; want write_file", post.ToolName)
	}
	var toolResp postToolUseResponsePayload
	if err := json.Unmarshal(post.ToolResponse, &toolResp); err != nil {
		t.Fatalf("decode tool_response: %v", err)
	}
	if toolResp.Err != "" {
		t.Errorf("PostToolUse tool_response.err = %q; want empty (tool succeeded)", toolResp.Err)
	}
}

// --- PreToolUse deny blocks the tool, turn still completes -----------------

// TestHooks_PreToolUseDeny_BlocksToolCall proves a PreToolUse hook can deny
// a call before it ever reaches the approval pipeline: the tool never
// executes (no note.txt written) and the model gets the hook's reason as
// the tool_result, exactly like an approval-stage deny (approval_test.go's
// TestManager_Approval_AskDenyIsToolResultNotAbort) — the turn still
// completes normally.
func TestHooks_PreToolUseDeny_BlocksToolCall(t *testing.T) {
	skipWithoutPOSIXTools(t)
	env := newHooksTestEnv(t)
	denyHandler := hooks.Handler{
		Type: hooks.HandlerCommand,
		Command: `cat <<'EOF'
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"blocked by test hook"}}
EOF`,
	}
	env.writeConfig(hooks.Config{Hooks: hooks.EventMap{
		hooks.EventPreToolUse: {{Matcher: "*", Hooks: []hooks.Handler{denyHandler}}},
	}})
	state := map[string]hookStateEntry{}
	env.trustHandler(state, hooks.EventPreToolUse, "*", denyHandler, 0, 0)
	env.writeState(state)
	hr := env.discover()

	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "should never land")}},
		fake.Step{Text: "ok"},
	)
	m := NewManager("th_hooks_pre_deny", provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()), WithHooks(hr),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	go func() { _ = m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)

	if _, err := os.Stat(filepath.Join(roots.WorkingDir, "note.txt")); err == nil {
		t.Fatal("write_file executed despite a PreToolUse deny")
	}
}

// --- the §4 invariant 1 test: PreToolUse "allow" can never override a ------
// --- policy deny — it only rewrites args, it never grants execution -------

// TestHooks_PreToolUseAllow_CannotOverridePolicyDeny is the invariant this
// unit's beforeToolCall wiring must preserve structurally (spec:
// agora-spec-approvals.md §4 invariant 1, "policy deny is final... hooks
// may only restrict further"): a PreToolUse hook returns allow+updatedInput
// (§2.1's only valid non-block outcome), but the Manager's policy denies
// KindPatch outright. The write MUST still be denied — PreToolUse fires
// BEFORE approval.Decide and its "allow" only rewrites tool args (see
// approval.go's beforeToolCall doc comment); it has no mechanism to force
// HookContinue-with-Deny-false past the real policy decision that runs
// after it.
func TestHooks_PreToolUseAllow_CannotOverridePolicyDeny(t *testing.T) {
	skipWithoutPOSIXTools(t)
	env := newHooksTestEnv(t)
	echoOut := filepath.Join(env.projectDir, "pre-allow.stdin.json")
	// Bare allow is Failed per §2.1 (codex-strict) — a valid allow REQUIRES
	// updatedInput; echo the same args back unchanged as updatedInput so
	// this is a well-formed "allow" outcome, not a Failed handler that
	// would trivially contribute nothing to the aggregate anyway.
	allowHandler := hooks.Handler{
		Type:    hooks.HandlerCommand,
		Command: fmt.Sprintf(`tee %q | python3 -c "import json,sys; d=json.load(sys.stdin); print(json.dumps({'hookSpecificOutput':{'hookEventName':'PreToolUse','permissionDecision':'allow','updatedInput':d['tool_input']}}))"`, echoOut),
	}
	env.writeConfig(hooks.Config{Hooks: hooks.EventMap{
		hooks.EventPreToolUse: {{Matcher: "*", Hooks: []hooks.Handler{allowHandler}}},
	}})
	state := map[string]hookStateEntry{}
	env.trustHandler(state, hooks.EventPreToolUse, "*", allowHandler, 0, 0)
	env.writeState(state)
	hr := env.discover()

	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "must stay denied")}},
		fake.Step{Text: "ok"},
	)
	policy := allowAllPolicy()
	policy[contracts.KindPatch] = contracts.PolicyDeny // the base policy decision this test pins against.
	m := NewManager("th_hooks_invariant", provider,
		WithRoots(roots), WithPolicy(policy), WithHooks(hr),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	go func() { _ = m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)

	// Prove the hook genuinely ran (and said allow) — the point of this
	// test is that saying allow was NOT ENOUGH, not that the hook never fired.
	waitForFile(t, echoOut, testTimeout)

	if _, err := os.Stat(filepath.Join(roots.WorkingDir, "note.txt")); err == nil {
		t.Fatal("write_file executed despite PolicyDeny — a PreToolUse allow bypassed the policy-deny invariant")
	}
}

// --- PermissionRequest tightens an otherwise-auto-allowed call -----------

// TestHooks_PermissionRequestDeny_TightensAnAutoAllowedCall exercises the
// restricting (uncontroversial) direction of bridge.go's
// ApplyPermissionRequest: a PermissionRequest hook denies a call the base
// policy would have auto-allowed. Spec §2.2 + agora-spec-approvals.md §4
// invariant 1 ("hooks... can only be more restrictive than policy").
func TestHooks_PermissionRequestDeny_TightensAnAutoAllowedCall(t *testing.T) {
	skipWithoutPOSIXTools(t)
	env := newHooksTestEnv(t)
	denyHandler := hooks.Handler{
		Type: hooks.HandlerCommand,
		Command: `cat <<'EOF'
{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"perm denied by hook"}}}
EOF`,
	}
	env.writeConfig(hooks.Config{Hooks: hooks.EventMap{
		hooks.EventPermissionRequest: {{Matcher: "*", Hooks: []hooks.Handler{denyHandler}}},
	}})
	state := map[string]hookStateEntry{}
	env.trustHandler(state, hooks.EventPermissionRequest, "*", denyHandler, 0, 0)
	env.writeState(state)
	hr := env.discover()

	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "must stay denied too")}},
		fake.Step{Text: "ok"},
	)
	m := NewManager("th_hooks_pr_deny", provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()) /* KindPatch=auto */, WithHooks(hr),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	go func() { _ = m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)

	if _, err := os.Stat(filepath.Join(roots.WorkingDir, "note.txt")); err == nil {
		t.Fatal("write_file executed despite a PermissionRequest deny (base policy was auto-allow)")
	}
}

// --- untrusted hook does not run -------------------------------------------

// TestHooks_UntrustedHandler_DoesNotRun proves trust.go's fail-closed
// ResolveTrust is actually honored end to end: a hooks.json handler with NO
// matching entry in hooks-state.json must never execute (spec §4.4:
// "absent -> Untrusted"; Untrusted -> not Runnable) — its side effect (the
// stdin file it would have written) must never appear, and — because it
// never ran — it must not block the tool call either (Untrusted handlers
// are skipped entirely, not treated as a deny).
func TestHooks_UntrustedHandler_DoesNotRun(t *testing.T) {
	env := newHooksTestEnv(t)
	sideEffect := filepath.Join(env.projectDir, "untrusted.ran")
	untrustedHandler := hooks.Handler{Type: hooks.HandlerCommand, Command: catCommand(sideEffect)}
	env.writeConfig(hooks.Config{Hooks: hooks.EventMap{
		hooks.EventPreToolUse: {{Matcher: "*", Hooks: []hooks.Handler{untrustedHandler}}},
	}})
	// Deliberately write NO state file / NO trust entry for this handler.
	hr := env.discover()

	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "untrusted hook must not block this")}},
		fake.Step{Text: "ok"},
	)
	m := NewManager("th_hooks_untrusted", provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()), WithHooks(hr),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	go func() { _ = m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)

	// The untrusted hook must not have run at all.
	if _, err := os.Stat(sideEffect); err == nil {
		t.Fatal("untrusted PreToolUse handler ran (wrote its side-effect file) — trust gate did not hold")
	}
	// And, because it never ran (not even a "Failed"/"Blocked" outcome),
	// the call must have proceeded normally under the auto-allow policy.
	got, err := os.ReadFile(filepath.Join(roots.WorkingDir, "note.txt"))
	if err != nil || string(got) != "untrusted hook must not block this" {
		t.Fatalf("write_file did not execute normally: content=%q err=%v", got, err)
	}
}

// --- async hook does not block the turn -----------------------------------

// TestHooks_AsyncPostToolUse_DoesNotBlockTheTurn proves async:true handlers
// are genuinely fire-and-forget on the live turn path (spec §1.4): the
// handler sleeps well past this test's turn-completion deadline before
// writing its side effect, so the turn completing before that file exists
// is the proof; a bounded poll afterward (timing-tolerant, no fixed sleep
// in the assertion path) confirms the async handler eventually did run.
func TestHooks_AsyncPostToolUse_DoesNotBlockTheTurn(t *testing.T) {
	skipWithoutPOSIXTools(t)
	env := newHooksTestEnv(t)
	sideEffect := filepath.Join(env.projectDir, "async.ran")
	asyncHandler := hooks.Handler{
		Type:    hooks.HandlerCommand,
		Command: fmt.Sprintf("sleep 1 && %s", catCommand(sideEffect)),
		Async:   true,
	}
	env.writeConfig(hooks.Config{Hooks: hooks.EventMap{
		hooks.EventPostToolUse: {{Matcher: "*", Hooks: []hooks.Handler{asyncHandler}}},
	}})
	state := map[string]hookStateEntry{}
	env.trustHandler(state, hooks.EventPostToolUse, "*", asyncHandler, 0, 0)
	env.writeState(state)
	hr := env.discover()

	roots := managerTestRoots(t)
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{writeFileCall("1", "note.txt", "async should not block")}},
		fake.Step{Text: "ok"},
	)
	m := NewManager("th_hooks_async", provider,
		WithRoots(roots), WithPolicy(allowAllPolicy()), WithHooks(hr),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	go func() { _ = m.Run(context.Background(), in, out) }()

	start := time.Now()
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write note.txt"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	elapsed := time.Since(start)
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)

	if elapsed >= 900*time.Millisecond {
		t.Fatalf("turn took %v to complete — looks like it waited on the async (1s-sleep) hook", elapsed)
	}
	if _, err := os.Stat(sideEffect); err == nil {
		t.Fatal("async hook's side effect already exists immediately after turn completion — suspiciously fast for a 1s-sleep handler; the turn may have waited after all")
	}
	waitForFile(t, sideEffect, 3*time.Second)
}

// --- SessionStart / UserPromptSubmit / Stop fire ---------------------------

// TestHooks_SessionStart_UserPromptSubmit_Stop_AllFire is a single fixture
// covering the three remaining wired events: SessionStart (once per Run),
// UserPromptSubmit (once per turn-starting user_message), and Stop (once
// per successfully-completed turn) — each a real command hook writing its
// stdin to a distinct file, asserted for the event-specific shape spec §2.6/
// §2.7/§2.9-10 define.
func TestHooks_SessionStart_UserPromptSubmit_Stop_AllFire(t *testing.T) {
	skipWithoutPOSIXTools(t)
	env := newHooksTestEnv(t)
	sessionOut := filepath.Join(env.projectDir, "session-start.json")
	promptOut := filepath.Join(env.projectDir, "prompt-submit.json")
	stopOut := filepath.Join(env.projectDir, "stop.json")

	sessionHandler := hooks.Handler{Type: hooks.HandlerCommand, Command: catCommand(sessionOut)}
	promptHandler := hooks.Handler{Type: hooks.HandlerCommand, Command: catCommand(promptOut)}
	stopHandler := hooks.Handler{Type: hooks.HandlerCommand, Command: catCommand(stopOut)}
	env.writeConfig(hooks.Config{Hooks: hooks.EventMap{
		hooks.EventSessionStart:     {{Matcher: "*", Hooks: []hooks.Handler{sessionHandler}}},
		hooks.EventUserPromptSubmit: {{Hooks: []hooks.Handler{promptHandler}}}, // matcher ignored for this event
		hooks.EventStop:             {{Hooks: []hooks.Handler{stopHandler}}},   // matcher ignored for this event
	}})
	state := map[string]hookStateEntry{}
	env.trustHandler(state, hooks.EventSessionStart, "*", sessionHandler, 0, 0)
	env.trustHandler(state, hooks.EventUserPromptSubmit, "", promptHandler, 0, 0)
	env.trustHandler(state, hooks.EventStop, "", stopHandler, 0, 0)
	env.writeState(state)
	hr := env.discover()

	roots := managerTestRoots(t)
	provider := fake.NewProvider(fake.Step{Text: "hello from the model"})
	m := NewManager("th_hooks_lifecycle", provider,
		WithRoots(roots), WithHooks(hr), WithContextEngine(false),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	go func() { _ = m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "say hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)

	var sess sessionStartInput
	readJSONFile(t, sessionOut, &sess)
	if sess.HookEventName != "SessionStart" || sess.Source != "startup" {
		t.Errorf("SessionStart input = %+v; want hook_event_name=SessionStart source=startup", sess)
	}

	var prompt userPromptSubmitInput
	readJSONFile(t, promptOut, &prompt)
	if prompt.Prompt != "say hi" {
		t.Errorf("UserPromptSubmit prompt = %q; want %q", prompt.Prompt, "say hi")
	}

	var stop stopInput
	readJSONFile(t, stopOut, &stop)
	if stop.LastAssistantMessage == nil || *stop.LastAssistantMessage != "hello from the model" {
		t.Errorf("Stop last_assistant_message = %v; want %q", stop.LastAssistantMessage, "hello from the model")
	}
}
