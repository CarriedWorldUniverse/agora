package hooks

import "testing"

// Every table below is transcribed directly from agora-spec-hooks.md §2's
// per-event I/O contracts (DoD: "per-event contract tests straight from
// hooks §2 — they are written as test tables").

func TestInterpretPreToolUse_Contract(t *testing.T) {
	cases := []struct {
		name             string
		exitCode         int
		stdout           string
		stderr           string
		wantStatus       RunStatus
		wantBlock        bool
		wantReason       string
		wantPermDecision string
		wantUpdatedInput string
	}{
		{
			name:     "exit0 plain text ignored (not in the plain-text-context exception list)",
			exitCode: 0, stdout: "just some log noise",
			wantStatus: RunCompleted,
		},
		{
			name:     "exit0 malformed JSON -> Failed",
			exitCode: 0, stdout: `{"not":`,
			wantStatus: RunFailed,
		},
		{
			name:       "hookSpecificOutput deny with reason -> blocks",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"forbidden path"}}`,
			wantStatus: RunCompleted, wantBlock: true, wantReason: "forbidden path", wantPermDecision: "deny",
		},
		{
			name:       "deny requires non-empty reason -> Failed if empty",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`,
			wantStatus: RunFailed,
		},
		{
			name:       "allow paired with updatedInput is valid",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"cmd":"ls"}}}`,
			wantStatus: RunCompleted, wantPermDecision: "allow", wantUpdatedInput: `{"cmd":"ls"}`,
		},
		{
			name:       "bare allow (no updatedInput) -> Failed in codex, agora keeps that",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`,
			wantStatus: RunFailed,
		},
		{
			name:       "ask -> Failed in codex",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask"}}`,
			wantStatus: RunFailed,
		},
		{
			name:       "legacy top-level decision:block + reason also blocks",
			exitCode:   0,
			stdout:     `{"decision":"block","reason":"legacy compat block"}`,
			wantStatus: RunCompleted, wantBlock: true, wantReason: "legacy compat block",
		},
		{
			name:       "legacy decision:block with no reason -> Failed",
			exitCode:   0,
			stdout:     `{"decision":"block"}`,
			wantStatus: RunFailed,
		},
		{
			name:       "universal continue:false is rejected for this event",
			exitCode:   0,
			stdout:     `{"continue":false}`,
			wantStatus: RunFailed,
		},
		{
			name:     "exit 2 blocks with trimmed stderr as reason",
			exitCode: 2, stderr: "  denied by policy  \n",
			wantStatus: RunBlocked, wantBlock: true, wantReason: "denied by policy",
		},
		{
			name:     "exit 2 with empty stderr -> Failed",
			exitCode: 2, stderr: "   ",
			wantStatus: RunFailed,
		},
		{
			name:       "any other exit -> Failed, non-fatal",
			exitCode:   1,
			wantStatus: RunFailed,
		},
		{
			name:       "additionalContext injected without a permissionDecision",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"extra context"}}`,
			wantStatus: RunCompleted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InterpretPreToolUse(tc.exitCode, []byte(tc.stdout), []byte(tc.stderr))
			if out.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", out.Status, tc.wantStatus)
			}
			if out.Block != tc.wantBlock {
				t.Errorf("Block = %v, want %v", out.Block, tc.wantBlock)
			}
			if out.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", out.Reason, tc.wantReason)
			}
			if out.PermissionDecision != tc.wantPermDecision {
				t.Errorf("PermissionDecision = %q, want %q", out.PermissionDecision, tc.wantPermDecision)
			}
			if tc.wantUpdatedInput != "" && string(out.UpdatedInput) != tc.wantUpdatedInput {
				t.Errorf("UpdatedInput = %s, want %s", out.UpdatedInput, tc.wantUpdatedInput)
			}
		})
	}
}

func TestInterpretPermissionRequest_Contract(t *testing.T) {
	cases := []struct {
		name         string
		exitCode     int
		stdout       string
		stderr       string
		wantStatus   RunStatus
		wantBehavior string
	}{
		{
			name:       "allow auto-approves",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`,
			wantStatus: RunCompleted, wantBehavior: "allow",
		},
		{
			name:       "deny auto-rejects",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"no"}}}`,
			wantStatus: RunCompleted, wantBehavior: "deny",
		},
		{
			name:       "no decision -> falls through (empty behavior)",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PermissionRequest"}}`,
			wantStatus: RunCompleted, wantBehavior: "",
		},
		{
			name:       "updatedInput on decision -> fail closed (reserved, unsupported)",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":{"x":1}}}}`,
			wantStatus: RunFailed,
		},
		{
			name:       "interrupt:true -> fail closed (reserved, unsupported)",
			exitCode:   0,
			stdout:     `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","interrupt":true}}}`,
			wantStatus: RunFailed,
		},
		{
			name:     "exit 2 -> deny with stderr message",
			exitCode: 2, stderr: "network egress blocked",
			wantStatus: RunBlocked, wantBehavior: "deny",
		},
		{
			name:     "exit 2 empty stderr -> Failed",
			exitCode: 2, stderr: "",
			wantStatus: RunFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InterpretPermissionRequest(tc.exitCode, []byte(tc.stdout), []byte(tc.stderr))
			if out.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", out.Status, tc.wantStatus)
			}
			if out.PRBehavior != tc.wantBehavior {
				t.Errorf("PRBehavior = %q, want %q", out.PRBehavior, tc.wantBehavior)
			}
		})
	}
}

func TestInterpretPostToolUse_Contract(t *testing.T) {
	cases := []struct {
		name       string
		exitCode   int
		stdout     string
		stderr     string
		wantStatus RunStatus
		wantBlock  bool
		wantReason string
		wantStop   bool
	}{
		{
			name:     "decision:block with reason -> feedback to model",
			exitCode: 0, stdout: `{"decision":"block","reason":"bad output"}`,
			wantStatus: RunCompleted, wantBlock: true, wantReason: "bad output",
		},
		{
			name:     "block requires non-empty reason",
			exitCode: 0, stdout: `{"decision":"block"}`,
			wantStatus: RunFailed,
		},
		{
			name:     "continue:false stops the turn with reason",
			exitCode: 0, stdout: `{"continue":false,"stopReason":"halt"}`,
			wantStatus: RunCompleted, wantStop: true,
		},
		{
			name:     "exit 2 blocks with stderr reason",
			exitCode: 2, stderr: "post-check failed",
			wantStatus: RunBlocked, wantBlock: true, wantReason: "post-check failed",
		},
		{
			name:     "additionalContext carried through",
			exitCode: 0, stdout: `{"hookSpecificOutput":{"additionalContext":"note"}}`,
			wantStatus: RunCompleted,
		},
		{
			name:       "other exit -> Failed",
			exitCode:   7,
			wantStatus: RunFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InterpretPostToolUse(tc.exitCode, []byte(tc.stdout), []byte(tc.stderr))
			if out.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", out.Status, tc.wantStatus)
			}
			if out.Block != tc.wantBlock {
				t.Errorf("Block = %v, want %v", out.Block, tc.wantBlock)
			}
			if out.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", out.Reason, tc.wantReason)
			}
			gotStop := !out.Continue
			if gotStop != tc.wantStop {
				t.Errorf("stopped(!Continue) = %v, want %v", gotStop, tc.wantStop)
			}
		})
	}
}

func TestInterpretCompact_Contract(t *testing.T) {
	cases := []struct {
		name       string
		exitCode   int
		stdout     string
		stderr     string
		wantStatus RunStatus
		wantStop   bool
	}{
		{
			name:     "continue:false aborts/halts compaction",
			exitCode: 0, stdout: `{"continue":false}`,
			wantStatus: RunCompleted, wantStop: true,
		},
		{
			name:     "any decision key is invalid for this event",
			exitCode: 0, stdout: `{"decision":"block","reason":"x"}`,
			wantStatus: RunFailed,
		},
		{
			name:     "plain universal-only output is fine",
			exitCode: 0, stdout: `{"systemMessage":"compacting"}`,
			wantStatus: RunCompleted,
		},
		{
			name:     "exit 2 blocks with stderr reason (uniform convention)",
			exitCode: 2, stderr: "refuse compaction",
			wantStatus: RunBlocked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InterpretCompact(tc.exitCode, []byte(tc.stdout), []byte(tc.stderr))
			if out.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", out.Status, tc.wantStatus)
			}
			gotStop := !out.Continue
			if gotStop != tc.wantStop {
				t.Errorf("stopped(!Continue) = %v, want %v", gotStop, tc.wantStop)
			}
		})
	}
}

func TestInterpretSessionStart_Contract(t *testing.T) {
	cases := []struct {
		name       string
		exitCode   int
		stdout     string
		wantStatus RunStatus
		wantCtx    string
		wantStop   bool
	}{
		{
			name:     "plain stdout becomes additionalContext",
			exitCode: 0, stdout: "welcome back",
			wantStatus: RunCompleted, wantCtx: "welcome back",
		},
		{
			name:     "hookSpecificOutput.additionalContext honored",
			exitCode: 0, stdout: `{"hookSpecificOutput":{"additionalContext":"structured ctx"}}`,
			wantStatus: RunCompleted, wantCtx: "structured ctx",
		},
		{
			name:     "continue:false honored (stops)",
			exitCode: 0, stdout: `{"continue":false}`,
			wantStatus: RunCompleted, wantStop: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InterpretSessionStart(tc.exitCode, []byte(tc.stdout), nil)
			if out.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", out.Status, tc.wantStatus)
			}
			if out.AdditionalContext != tc.wantCtx {
				t.Errorf("AdditionalContext = %q, want %q", out.AdditionalContext, tc.wantCtx)
			}
			gotStop := !out.Continue
			if gotStop != tc.wantStop {
				t.Errorf("stopped(!Continue) = %v, want %v", gotStop, tc.wantStop)
			}
		})
	}
}

func TestInterpretSubagentStart_Contract_ContinueFalseIgnored(t *testing.T) {
	// §2.8: "like SessionStart but continue:false is IGNORED
	// (context-injection only)."
	out := InterpretSubagentStart(0, []byte(`{"continue":false}`), nil)
	if !out.Continue {
		t.Error("SubagentStart must IGNORE continue:false")
	}
	out = InterpretSubagentStart(0, []byte("hello subagent"), nil)
	if out.AdditionalContext != "hello subagent" {
		t.Errorf("plain text must become additionalContext, got %q", out.AdditionalContext)
	}
}

func TestInterpretUserPromptSubmit_Contract(t *testing.T) {
	cases := []struct {
		name       string
		exitCode   int
		stdout     string
		stderr     string
		wantStatus RunStatus
		wantCtx    string
		wantBlock  bool
		wantStop   bool
	}{
		{
			name:     "plain stdout -> additionalContext",
			exitCode: 0, stdout: "inject this",
			wantStatus: RunCompleted, wantCtx: "inject this",
		},
		{
			name:     "decision:block + reason blocks the prompt",
			exitCode: 0, stdout: `{"decision":"block","reason":"missing info"}`,
			wantStatus: RunCompleted, wantBlock: true,
		},
		{
			name:     "continue:false stops",
			exitCode: 0, stdout: `{"continue":false}`,
			wantStatus: RunCompleted, wantStop: true,
		},
		{
			name:     "exit 2 blocks with stderr",
			exitCode: 2, stderr: "prompt rejected",
			wantStatus: RunBlocked, wantBlock: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InterpretUserPromptSubmit(tc.exitCode, []byte(tc.stdout), []byte(tc.stderr))
			if out.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", out.Status, tc.wantStatus)
			}
			if out.AdditionalContext != tc.wantCtx {
				t.Errorf("AdditionalContext = %q, want %q", out.AdditionalContext, tc.wantCtx)
			}
			if out.Block != tc.wantBlock {
				t.Errorf("Block = %v, want %v", out.Block, tc.wantBlock)
			}
			gotStop := !out.Continue
			if gotStop != tc.wantStop {
				t.Errorf("stopped = %v, want %v", gotStop, tc.wantStop)
			}
		})
	}
}

func TestInterpretStop_Contract(t *testing.T) {
	cases := []struct {
		name       string
		exitCode   int
		stdout     string
		stderr     string
		wantStatus RunStatus
		wantBlock  bool
		wantReason string
		wantStop   bool
	}{
		{
			name:     "decision:block reason becomes a continuation prompt",
			exitCode: 0, stdout: `{"decision":"block","reason":"keep going, check X"}`,
			wantStatus: RunCompleted, wantBlock: true, wantReason: "keep going, check X",
		},
		{
			name:     "continue:false ends the turn, overrides block semantics at this layer",
			exitCode: 0, stdout: `{"continue":false}`,
			wantStatus: RunCompleted, wantStop: true,
		},
		{
			name:     "exit 2 blocks with stderr as the continuation prompt",
			exitCode: 2, stderr: "not done yet",
			wantStatus: RunBlocked, wantBlock: true, wantReason: "not done yet",
		},
		{
			name:     "block requires non-empty reason",
			exitCode: 0, stdout: `{"decision":"block"}`,
			wantStatus: RunFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InterpretStop(tc.exitCode, []byte(tc.stdout), []byte(tc.stderr))
			if out.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", out.Status, tc.wantStatus)
			}
			if out.Block != tc.wantBlock {
				t.Errorf("Block = %v, want %v", out.Block, tc.wantBlock)
			}
			if out.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", out.Reason, tc.wantReason)
			}
			gotStop := !out.Continue
			if gotStop != tc.wantStop {
				t.Errorf("stopped = %v, want %v", gotStop, tc.wantStop)
			}
		})
	}
}
