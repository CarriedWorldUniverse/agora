package hooks

import (
	"encoding/json"
	"testing"
)

func TestHandler_Normalize_DefaultAndFloor(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"unset defaults to 600", 0, DefaultTimeoutSeconds},
		{"below floor clamps to 1", -5, MinTimeoutSeconds},
		{"floor value passes through", 1, 1},
		{"ordinary value passes through", 30, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Handler{Timeout: tc.in}.Normalize()
			if h.Timeout != tc.want {
				t.Errorf("Timeout = %d, want %d", h.Timeout, tc.want)
			}
		})
	}
}

func TestHandler_EffectiveCommand(t *testing.T) {
	h := Handler{Command: "echo unix", CommandWindows: "echo win"}
	if got := h.EffectiveCommand("linux"); got != "echo unix" {
		t.Errorf("linux EffectiveCommand = %q, want echo unix", got)
	}
	if got := h.EffectiveCommand("windows"); got != "echo win" {
		t.Errorf("windows EffectiveCommand = %q, want echo win", got)
	}
	noWin := Handler{Command: "echo only"}
	if got := noWin.EffectiveCommand("windows"); got != "echo only" {
		t.Errorf("windows with no override should fall back to Command, got %q", got)
	}
}

func TestConfig_JSONRoundTrip(t *testing.T) {
	cfg := Config{
		Description: "test config",
		Hooks: EventMap{
			EventPreToolUse: {
				{Matcher: "Bash", Hooks: []Handler{{Type: HandlerCommand, Command: "echo hi", Timeout: 30, Async: true}}},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Config
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	h := got.Hooks[EventPreToolUse][0].Hooks[0]
	if h.Command != "echo hi" || h.Timeout != 30 || !h.Async {
		t.Errorf("round-tripped handler = %+v, want the original", h)
	}
}
