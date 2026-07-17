package tui

import "testing"

// finding #3 (security): agent/tool-originated content printed to the real
// terminal scrollback must have its ANSI/OSC control bytes neutralized —
// an OSC 52 clipboard-write, a cursor-repositioning CSI sequence, or an SGR
// styling sequence forwarded verbatim would let prompt-injected tool output
// hijack the operator's real terminal.

func TestSanitizeTerminalText_StripsOSC52ClipboardWrite(t *testing.T) {
	// ESC ] 52 ; c ; <base64> BEL
	in := "innocuous text \x1b]52;c;ZXZpbA==\x07 more text"
	got := sanitizeTerminalText(in)
	if got != "innocuous text  more text" {
		t.Fatalf("got %q", got)
	}
	if containsESC(got) {
		t.Fatalf("raw ESC survived sanitization: %q", got)
	}
}

func TestSanitizeTerminalText_StripsCursorMoveCSI(t *testing.T) {
	// ESC [ <n> A moves cursor up n lines (could overwrite prior scrollback).
	in := "line one\x1b[5Aoverwrite attempt"
	got := sanitizeTerminalText(in)
	if got != "line oneoverwrite attempt" {
		t.Fatalf("got %q", got)
	}
	if containsESC(got) {
		t.Fatalf("raw ESC survived sanitization: %q", got)
	}
}

func TestSanitizeTerminalText_StripsSGRStyling(t *testing.T) {
	// ESC [ 1 ; 31 m ... ESC [ 0 m — bold red text, title-spoof-adjacent.
	in := "\x1b[1;31mFAKE SYSTEM ALERT\x1b[0m"
	got := sanitizeTerminalText(in)
	if got != "FAKE SYSTEM ALERT" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeTerminalText_KeepsOrdinaryTextAndNewlines(t *testing.T) {
	in := "plain line one\nplain line two\ttabbed"
	if got := sanitizeTerminalText(in); got != in {
		t.Fatalf("got %q, want unchanged %q", got, in)
	}
}

func TestSanitizeTerminalText_DropsBareC0AndC1Controls(t *testing.T) {
	in := "a\x00b\x07c\x1fde" // NUL, BEL, US, a C1 control
	got := sanitizeTerminalText(in)
	if got != "abcde" {
		t.Fatalf("got %q", got)
	}
}

// TestStream_AppendSanitizesInjectedEscapeSequences is the end-to-end
// boundary test the brief asks for: content flowing through StreamState
// (the real path from an agent delta to the printed/committed output)
// renders as inert text with no raw ESC byte anywhere in the result.
func TestStream_AppendSanitizesInjectedEscapeSequences(t *testing.T) {
	s := NewStreamState()
	s.Append("safe start \x1b]52;c;evil\x07\x1b[2J\x1b[1;31mspoofed\x1b[0m safe end\n")
	committed := s.Finalize()
	if len(committed) != 1 {
		t.Fatalf("Finalize() = %v, want 1 line", committed)
	}
	if containsESC(committed[0]) {
		t.Fatalf("committed line still has a raw ESC byte: %q", committed[0])
	}
	want := "safe start spoofed safe end"
	if committed[0] != want {
		t.Fatalf("got %q, want %q", committed[0], want)
	}
}

func containsESC(s string) bool {
	for _, r := range s {
		if r == 0x1b {
			return true
		}
	}
	return false
}
