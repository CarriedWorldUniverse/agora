package main

import (
	"strings"
	"testing"
)

// terminalRestoreEscapes is the load-bearing string that unfucks the
// operator's terminal after a panic killed agora without bubbletea's
// cleanup running. If any of these sequences gets dropped in a
// future edit, a class of crashes silently regresses to the original
// "mouse-tracking spam on dead terminal" symptom. Guarding each
// individual escape so a partial accidental delete fails the test.
func TestTerminalRestoreEscapes_AllRequiredSequences(t *testing.T) {
	required := map[string]string{
		"\x1b[?1003l": "disable mouse-all-motion",
		"\x1b[?1006l": "disable SGR mouse encoding",
		"\x1b[?1000l": "disable basic X10 mouse tracking",
		"\x1b[?1049l": "exit alt-screen → restore main-screen",
		"\x1b[?25h":   "show cursor",
	}
	for seq, what := range required {
		if !strings.Contains(terminalRestoreEscapes, seq) {
			t.Errorf("terminalRestoreEscapes missing %q (%s) — operator's terminal will stay broken after a crash that triggered this mode",
				seq, what)
		}
	}
}

// Belt-and-braces: confirm the constant ends with the cursor-show
// sequence specifically. Order doesn't matter for terminal behaviour
// (escapes are independent state-bits), but if a future edit
// reshuffles things, the cursor-show being last is a good final
// invariant — operator sees a normal cursor immediately when escape
// flush completes.
func TestTerminalRestoreEscapes_EndsWithCursorShow(t *testing.T) {
	if !strings.HasSuffix(terminalRestoreEscapes, "\x1b[?25h") {
		t.Errorf("terminalRestoreEscapes should end with cursor-show; got %q",
			terminalRestoreEscapes)
	}
}
