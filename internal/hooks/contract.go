package hooks

import (
	"bytes"
	"encoding/json"
	"strings"
)

// --- common stdin (§2, top) ---------------------------------------------

// CommonInput carries the fields present on every event's stdin. Field
// names snake_case per spec §2. TranscriptPath is nullable (a JSON `null`
// unmarshals to nil), matching a *string here rather than "".
type CommonInput struct {
	SessionID      string  `json:"session_id"`
	Cwd            string  `json:"cwd"`
	TranscriptPath *string `json:"transcript_path"`
	HookEventName  string  `json:"hook_event_name"`
	Model          string  `json:"model"`
	// PermissionMode is omitted for PreCompact/PostCompact (§2.4/§2.5); see
	// EventName.HasPermissionMode.
	PermissionMode string `json:"permission_mode,omitempty"`
	// TurnID is present on turn-scoped events only (§2, EventName.HasTurnID).
	TurnID string `json:"turn_id,omitempty"`
	// AgentID/AgentType are optional, present when an event fires inside a
	// subagent (§2).
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
}

// --- RunStatus / OutputKind (§3) ----------------------------------------

// RunStatus is the per-handler run status vocabulary. Spec §3.
type RunStatus string

const (
	RunRunning   RunStatus = "Running"
	RunCompleted RunStatus = "Completed"
	RunFailed    RunStatus = "Failed"
	RunBlocked   RunStatus = "Blocked"
	RunStopped   RunStatus = "Stopped"
)

// OutputKind classifies a handler's reported output. Spec §3.
type OutputKind string

const (
	KindContext  OutputKind = "Context"
	KindFeedback OutputKind = "Feedback"
	KindStop     OutputKind = "Stop"
	KindWarning  OutputKind = "Warning"
	KindError    OutputKind = "Error"
)

// --- raw stdout wire shape (universal + hookSpecificOutput, §2 top + 2.x) -

// prDecision is PermissionRequest's hookSpecificOutput.decision (§2.2).
type prDecision struct {
	Behavior           string          `json:"behavior"`
	Message            string          `json:"message,omitempty"`
	UpdatedInput       json.RawMessage `json:"updatedInput,omitempty"`
	UpdatedPermissions json.RawMessage `json:"updatedPermissions,omitempty"`
	Interrupt          bool            `json:"interrupt,omitempty"`
}

type hookSpecificOutput struct {
	HookEventName            string          `json:"hookEventName,omitempty"`
	PermissionDecision       string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
	AdditionalContext        string          `json:"additionalContext,omitempty"`
	Decision                 *prDecision     `json:"decision,omitempty"`
}

// rawStdout is the JSON shape parsed from a handler's stdout on exit 0
// (§2 top + per-event subsections). Not every field is honored by every
// event — event-specific Interpret* functions apply the per-event rules.
type rawStdout struct {
	Continue       *bool  `json:"continue,omitempty"`
	StopReason     string `json:"stopReason,omitempty"`
	SuppressOutput bool   `json:"suppressOutput,omitempty"`
	SystemMessage  string `json:"systemMessage,omitempty"`
	// Decision/Reason: legacy top-level Claude-compat block (§2.1: "Legacy
	// top-level decision:'block' + reason also blocks"; §2.3/§2.7 use the
	// same shape non-legacy).
	Decision             string              `json:"decision,omitempty"`
	Reason               string              `json:"reason,omitempty"`
	HookSpecificOutput   *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	UpdatedMCPToolOutput json.RawMessage     `json:"updatedMCPToolOutput,omitempty"`
}

func looksLikeJSON(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '{'
}

// HandlerOutcome is a single handler's parsed result for one event firing —
// the union of everything an Interpret* function might set. Only the
// fields relevant to the firing event are meaningful; aggregate.go's
// per-event Aggregate functions read only the fields their event defines.
type HandlerOutcome struct {
	Status RunStatus

	// Universal (§2 top), as actually honored for this event.
	Continue       bool // default true
	StopReason     string
	SuppressOutput bool
	SystemMessage  string

	// AdditionalContext: injected context (SessionStart/SubagentStart/
	// UserPromptSubmit plain-text fallback, or hookSpecificOutput.additionalContext).
	AdditionalContext string

	// Block: this handler's result blocks/denies (PreToolUse deny,
	// PostToolUse/UserPromptSubmit/Stop decision:block, exit 2).
	Block  bool
	Reason string

	// PreToolUse-only.
	PermissionDecision string // "allow" | "deny" | "" (ask/bare-allow -> Failed, see InterpretPreToolUse)
	UpdatedInput       json.RawMessage

	// PermissionRequest-only.
	PRBehavior string // "allow" | "deny" | ""
	PRMessage  string
}

// --- shared exit-code interpretation (§2 top) ---------------------------

// baseInterpret applies the uniform exit-code convention (§2, top) that
// every event shares, before event-specific rules are layered on:
//
//	exit 0    -> parse stdout as JSON if it looks like JSON (malformed ->
//	             Failed); else plain text, ignored except where
//	             contextOnPlainText is set (additionalContext).
//	exit 2    -> block/deny, trimmed stderr as reason (empty stderr -> Failed).
//	other     -> Failed (non-fatal: recorded, doesn't block).
func baseInterpret(exitCode int, stdout, stderr []byte, contextOnPlainText bool) (raw rawStdout, status RunStatus, plainContext string, blockReason string) {
	switch {
	case exitCode == 2:
		r := strings.TrimSpace(string(stderr))
		if r == "" {
			return rawStdout{}, RunFailed, "", ""
		}
		return rawStdout{}, RunBlocked, "", r

	case exitCode == 0:
		if looksLikeJSON(stdout) {
			var r rawStdout
			if err := json.Unmarshal(stdout, &r); err != nil {
				return rawStdout{}, RunFailed, "", ""
			}
			return r, RunCompleted, "", ""
		}
		if contextOnPlainText {
			if txt := strings.TrimSpace(string(stdout)); txt != "" {
				return rawStdout{}, RunCompleted, txt, ""
			}
		}
		return rawStdout{}, RunCompleted, "", ""

	default:
		return rawStdout{}, RunFailed, "", ""
	}
}

func continueOrDefault(c *bool) bool {
	if c == nil {
		return true
	}
	return *c
}

// --- §2.1 PreToolUse ------------------------------------------------------

// InterpretPreToolUse parses one handler's exit code/stdout/stderr for
// PreToolUse. Spec §2.1.
//
// Deviations from codex, both DELIBERATE per the spec's own invitation
// ("agora: consider..." / "flag as deliberate deviation"), resolved here
// to the NARROWER (codex-strict) reading rather than inventing new
// behavior (ground rule 7):
//   - bare `allow` (no updatedInput) -> Failed, exactly as codex. Not
//     honored as "skip-approval" — that would be a new decision surface the
//     spec only floats as a maybe.
//   - `ask` -> Failed, exactly as codex.
func InterpretPreToolUse(exitCode int, stdout, stderr []byte) HandlerOutcome {
	raw, status, _, blockReason := baseInterpret(exitCode, stdout, stderr, false)
	out := HandlerOutcome{Status: status, Continue: true}

	if status == RunBlocked {
		out.Block = true
		out.Reason = blockReason
		return out
	}
	if status != RunCompleted {
		return out
	}

	// Universal continue:false/stopReason/suppressOutput are explicitly
	// REJECTED for this event (§2.1): treat their presence as a malformed
	// response for this event -> Failed.
	if raw.Continue != nil && !*raw.Continue {
		out.Status = RunFailed
		return out
	}

	// Legacy top-level decision:"block" (Claude compat).
	if raw.Decision == "block" {
		if raw.Reason == "" {
			out.Status = RunFailed
			return out
		}
		out.Block = true
		out.Reason = raw.Reason
		return out
	}

	if raw.HookSpecificOutput != nil {
		hso := raw.HookSpecificOutput
		out.AdditionalContext = hso.AdditionalContext
		switch hso.PermissionDecision {
		case "":
			// no decision at all: fine, additionalContext (if any) still applies.
		case "deny":
			if hso.PermissionDecisionReason == "" {
				out.Status = RunFailed
				return out
			}
			out.Block = true
			out.Reason = hso.PermissionDecisionReason
			out.PermissionDecision = "deny"
		case "allow":
			if len(hso.UpdatedInput) == 0 {
				// bare allow -> Failed (codex-strict, see doc comment).
				out.Status = RunFailed
				return out
			}
			out.PermissionDecision = "allow"
			out.UpdatedInput = hso.UpdatedInput
		default:
			// "ask" or anything unrecognized -> Failed (codex-strict).
			out.Status = RunFailed
			return out
		}
	}
	return out
}

// --- §2.2 PermissionRequest -----------------------------------------------

// InterpretPermissionRequest parses one handler's exit code/stdout/stderr
// for PermissionRequest. Spec §2.2. updatedInput/updatedPermissions/
// interrupt on the decision object are reserved and FAIL CLOSED: their
// presence makes the response Failed rather than silently ignoring an
// unsupported escalation of scope (ground rule 6 — never loosen policy via
// an unimplemented field).
func InterpretPermissionRequest(exitCode int, stdout, stderr []byte) HandlerOutcome {
	raw, status, _, blockReason := baseInterpret(exitCode, stdout, stderr, false)
	out := HandlerOutcome{Status: status, Continue: true}

	if status == RunBlocked {
		out.Block = true
		out.Reason = blockReason
		out.PRBehavior = "deny"
		out.PRMessage = blockReason
		return out
	}
	if status != RunCompleted {
		return out
	}
	if raw.HookSpecificOutput == nil || raw.HookSpecificOutput.Decision == nil {
		return out // no decision -> fall through to normal approval flow.
	}
	d := raw.HookSpecificOutput.Decision
	if len(d.UpdatedInput) > 0 || len(d.UpdatedPermissions) > 0 || d.Interrupt {
		out.Status = RunFailed
		return out
	}
	switch d.Behavior {
	case "allow":
		out.PRBehavior = "allow"
		out.PRMessage = d.Message
	case "deny":
		out.PRBehavior = "deny"
		out.PRMessage = d.Message
		if out.PRMessage == "" {
			out.PRMessage = "denied by hook"
		}
	default:
		out.Status = RunFailed
	}
	return out
}

// --- §2.3 PostToolUse -------------------------------------------------

// InterpretPostToolUse parses one handler's exit code/stdout/stderr for
// PostToolUse. Spec §2.3. updatedMCPToolOutput is unsupported and "fails
// open" per spec — i.e. its presence does not fail the handler, it is just
// not applied (not modeled further; the daemon-side MCP unit owns that).
func InterpretPostToolUse(exitCode int, stdout, stderr []byte) HandlerOutcome {
	raw, status, _, blockReason := baseInterpret(exitCode, stdout, stderr, false)
	out := HandlerOutcome{Status: status, Continue: true}

	if status == RunBlocked {
		out.Block = true
		out.Reason = blockReason
		return out
	}
	if status != RunCompleted {
		return out
	}
	out.Continue = continueOrDefault(raw.Continue)
	out.StopReason = raw.StopReason
	out.SuppressOutput = raw.SuppressOutput
	out.SystemMessage = raw.SystemMessage
	if raw.Decision == "block" {
		if raw.Reason == "" {
			out.Status = RunFailed
			return out
		}
		out.Block = true
		out.Reason = raw.Reason
	}
	if raw.HookSpecificOutput != nil {
		out.AdditionalContext = raw.HookSpecificOutput.AdditionalContext
	}
	return out
}

// --- §2.4/§2.5 PreCompact / PostCompact -----------------------------------

// InterpretCompact parses one handler's exit code/stdout/stderr for
// PreCompact/PostCompact (shared shape: universal fields only, §2.4/§2.5).
// Any `decision` key present is invalid for these events.
func InterpretCompact(exitCode int, stdout, stderr []byte) HandlerOutcome {
	raw, status, _, blockReason := baseInterpret(exitCode, stdout, stderr, false)
	out := HandlerOutcome{Status: status, Continue: true}

	if status == RunBlocked {
		out.Block = true
		out.Reason = blockReason
		return out
	}
	if status != RunCompleted {
		return out
	}
	if raw.Decision != "" {
		out.Status = RunFailed
		return out
	}
	out.Continue = continueOrDefault(raw.Continue)
	out.StopReason = raw.StopReason
	out.SuppressOutput = raw.SuppressOutput
	out.SystemMessage = raw.SystemMessage
	return out
}

// --- §2.6 SessionStart / §2.8 SubagentStart -------------------------------

// InterpretSessionStart parses one handler's exit code/stdout/stderr for
// SessionStart. Spec §2.6: continue:false is honored (stops).
func InterpretSessionStart(exitCode int, stdout, stderr []byte) HandlerOutcome {
	return interpretSessionLike(exitCode, stdout, stderr, true)
}

// InterpretSubagentStart parses one handler's exit code/stdout/stderr for
// SubagentStart. Spec §2.8: "like SessionStart but continue:false is
// IGNORED (context-injection only)".
func InterpretSubagentStart(exitCode int, stdout, stderr []byte) HandlerOutcome {
	return interpretSessionLike(exitCode, stdout, stderr, false)
}

func interpretSessionLike(exitCode int, stdout, stderr []byte, honorStop bool) HandlerOutcome {
	raw, status, plainCtx, blockReason := baseInterpret(exitCode, stdout, stderr, true)
	out := HandlerOutcome{Status: status, Continue: true}

	if status == RunBlocked {
		// §2.6/§2.8 describe only the exit-0 output shape (universal +
		// additionalContext), but the exit-code convention itself is
		// declared uniform across all 10 events (§2, top) — applying it
		// here rather than inventing a SessionStart-specific carve-out is
		// the simplest spec-consistent reading (ground rule 7). Block is
		// reported like every other event; ContextAggregate decides what a
		// blocked SessionStart/SubagentStart means for the caller.
		out.Block = true
		out.Reason = blockReason
		return out
	}
	if status != RunCompleted {
		return out
	}
	if plainCtx != "" {
		out.AdditionalContext = plainCtx
		return out
	}
	if honorStop {
		out.Continue = continueOrDefault(raw.Continue)
	}
	out.StopReason = raw.StopReason
	out.SuppressOutput = raw.SuppressOutput
	out.SystemMessage = raw.SystemMessage
	if raw.HookSpecificOutput != nil {
		out.AdditionalContext = raw.HookSpecificOutput.AdditionalContext
	}
	return out
}

// --- §2.7 UserPromptSubmit -------------------------------------------------

// InterpretUserPromptSubmit parses one handler's exit code/stdout/stderr
// for UserPromptSubmit. Spec §2.7.
func InterpretUserPromptSubmit(exitCode int, stdout, stderr []byte) HandlerOutcome {
	raw, status, plainCtx, blockReason := baseInterpret(exitCode, stdout, stderr, true)
	out := HandlerOutcome{Status: status, Continue: true}

	if status == RunBlocked {
		out.Block = true
		out.Reason = blockReason
		return out
	}
	if status != RunCompleted {
		return out
	}
	if plainCtx != "" {
		out.AdditionalContext = plainCtx
		return out
	}
	out.Continue = continueOrDefault(raw.Continue)
	out.StopReason = raw.StopReason
	if raw.Decision == "block" {
		if raw.Reason == "" {
			out.Status = RunFailed
			return out
		}
		out.Block = true
		out.Reason = raw.Reason
	}
	if raw.HookSpecificOutput != nil {
		out.AdditionalContext = raw.HookSpecificOutput.AdditionalContext
	}
	return out
}

// --- §2.9/§2.10 SubagentStop / Stop ----------------------------------------

// InterpretStop parses one handler's exit code/stdout/stderr for Stop and
// SubagentStop (shared output shape, §2.9/§2.10). continue:false OVERRIDES
// a block (ends the turn outright); a plain block's Reason becomes a
// continuation prompt at the aggregation layer (aggregate.go).
func InterpretStop(exitCode int, stdout, stderr []byte) HandlerOutcome {
	raw, status, _, blockReason := baseInterpret(exitCode, stdout, stderr, false)
	out := HandlerOutcome{Status: status, Continue: true}

	if status == RunBlocked {
		out.Block = true
		out.Reason = blockReason
		return out
	}
	if status != RunCompleted {
		return out
	}
	out.Continue = continueOrDefault(raw.Continue)
	if raw.Decision == "block" {
		if raw.Reason == "" {
			out.Status = RunFailed
			return out
		}
		out.Block = true
		out.Reason = raw.Reason
	}
	return out
}
