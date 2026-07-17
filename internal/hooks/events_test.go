package hooks

import "testing"

func TestEventName_MatcherIgnored(t *testing.T) {
	cases := []struct {
		event EventName
		want  bool
	}{
		{EventPreToolUse, false},
		{EventPermissionRequest, false},
		{EventPostToolUse, false},
		{EventPreCompact, false},
		{EventPostCompact, false},
		{EventSessionStart, false},
		{EventUserPromptSubmit, true},
		{EventSubagentStart, false},
		{EventSubagentStop, false},
		{EventStop, true},
	}
	for _, tc := range cases {
		if got := tc.event.MatcherIgnored(); got != tc.want {
			t.Errorf("%s.MatcherIgnored() = %v, want %v", tc.event, got, tc.want)
		}
	}
}

func TestEventName_HasTurnID(t *testing.T) {
	cases := []struct {
		event EventName
		want  bool
	}{
		{EventPreToolUse, true},
		{EventPermissionRequest, true},
		{EventPostToolUse, true},
		{EventPreCompact, true},
		{EventPostCompact, true},
		{EventSessionStart, false},
		{EventUserPromptSubmit, true},
		{EventSubagentStart, false},
		{EventSubagentStop, true},
		{EventStop, true},
	}
	for _, tc := range cases {
		if got := tc.event.HasTurnID(); got != tc.want {
			t.Errorf("%s.HasTurnID() = %v, want %v", tc.event, got, tc.want)
		}
	}
}

func TestEventName_HasPermissionMode(t *testing.T) {
	if EventPreCompact.HasPermissionMode() {
		t.Error("PreCompact must not carry permission_mode (§2.4)")
	}
	if EventPostCompact.HasPermissionMode() {
		t.Error("PostCompact must not carry permission_mode (§2.5)")
	}
	if !EventPreToolUse.HasPermissionMode() {
		t.Error("PreToolUse must carry permission_mode")
	}
}

func TestEventName_ContextInjectingOnPlainText(t *testing.T) {
	want := map[EventName]bool{
		EventSessionStart:     true,
		EventSubagentStart:    true,
		EventUserPromptSubmit: true,
	}
	for _, e := range AllEvents {
		got := e.ContextInjectingOnPlainText()
		if got != want[e] {
			t.Errorf("%s.ContextInjectingOnPlainText() = %v, want %v", e, got, want[e])
		}
	}
}

func TestEventName_Valid(t *testing.T) {
	for _, e := range AllEvents {
		if !e.Valid() {
			t.Errorf("%s should be Valid", e)
		}
	}
	if EventName("Bogus").Valid() {
		t.Error("unrecognized event name should not be Valid")
	}
}

func TestEventName_TurnScoped(t *testing.T) {
	// §3: "Scope: SessionStart+SubagentStart = thread-scoped; all else turn-scoped."
	if EventSessionStart.TurnScoped() {
		t.Error("SessionStart is thread-scoped, not turn-scoped")
	}
	if EventSubagentStart.TurnScoped() {
		t.Error("SubagentStart is thread-scoped, not turn-scoped")
	}
	if !EventStop.TurnScoped() {
		t.Error("Stop is turn-scoped")
	}
}
