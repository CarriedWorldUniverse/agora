package hooks

// EventName is one of the 10 lifecycle hook events.
// Spec: agora-spec-hooks.md §1.2.
type EventName string

const (
	EventPreToolUse        EventName = "PreToolUse"
	EventPermissionRequest EventName = "PermissionRequest"
	EventPostToolUse       EventName = "PostToolUse"
	EventPreCompact        EventName = "PreCompact"
	EventPostCompact       EventName = "PostCompact"
	EventSessionStart      EventName = "SessionStart"
	EventUserPromptSubmit  EventName = "UserPromptSubmit"
	EventSubagentStart     EventName = "SubagentStart"
	EventSubagentStop      EventName = "SubagentStop"
	EventStop              EventName = "Stop"
)

// AllEvents is the fixed, canonical declaration order of the 10 events —
// used wherever a deterministic iteration over events is needed (e.g.
// assigning discovery-order sequence numbers out of a config map, ground
// rule 3: never rely on Go map-iteration order). This is exactly the
// event-map key list of §1.2, in the order written there.
var AllEvents = []EventName{
	EventPreToolUse,
	EventPermissionRequest,
	EventPostToolUse,
	EventPreCompact,
	EventPostCompact,
	EventSessionStart,
	EventUserPromptSubmit,
	EventSubagentStart,
	EventSubagentStop,
	EventStop,
}

// Valid reports whether e is one of the 10 recognized events.
func (e EventName) Valid() bool {
	for _, x := range AllEvents {
		if x == e {
			return true
		}
	}
	return false
}

// MatcherIgnored reports whether matchers are ignored (the event's handlers
// always run) for e. Spec §1.2: "Matchers apply to 8 of them;
// UserPromptSubmit and Stop ignore matchers (always run)." SubagentStop is
// NOT in this set — despite matchers being "ignored for Stop", §2.9/2.10
// explicitly says "Matcher ignored for Stop" only, so SubagentStop matches
// normally against agent_type (§1.5).
func (e EventName) MatcherIgnored() bool {
	return e == EventUserPromptSubmit || e == EventStop
}

// TurnScoped reports whether e's run scope is turn-scoped rather than
// thread-scoped. Spec §3: "Scope: SessionStart+SubagentStart = thread-scoped;
// all else turn-scoped."
func (e EventName) TurnScoped() bool {
	return e != EventSessionStart && e != EventSubagentStart
}

// HasTurnID reports whether e's common stdin fields include turn_id. Spec
// §2: "Turn-scoped events add turn_id" — SessionStart/SubagentStart do not
// (§2.6/§2.8 input lists omit it), matching TurnScoped's split exactly.
func (e EventName) HasTurnID() bool {
	return e.TurnScoped()
}

// HasPermissionMode reports whether e's common stdin fields include
// permission_mode. Spec §2.4/§2.5: "no permission_mode" for
// PreCompact/PostCompact; every other event carries it (either via the
// common-fields list §2, or explicitly in its own input list).
func (e EventName) HasPermissionMode() bool {
	return e != EventPreCompact && e != EventPostCompact
}

// ContextInjectingOnPlainText reports whether plain (non-JSON) stdout on
// exit 0 becomes additionalContext for e. Spec §2 (top): "plain text →
// ignored, EXCEPT SessionStart/SubagentStart/UserPromptSubmit where plain
// text becomes additionalContext."
func (e EventName) ContextInjectingOnPlainText() bool {
	return e == EventSessionStart || e == EventSubagentStart || e == EventUserPromptSubmit
}
