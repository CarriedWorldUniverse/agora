package tui

// sanitizeTerminalText strips C0/C1 control bytes and ANSI/OSC escape
// sequences from agent/tool-originated content before it is ever handed to
// a Cell/DiffCell/subject renderer or printed straight to the real
// terminal (finding #3, security): §0's design prints finalized cells to
// the terminal's own scrollback via tea.Println with NO alt-screen, and
// content along that path ultimately originates from the LLM or a tool —
// which can be prompt-injected with raw ANSI/OSC (OSC 52 clipboard-write,
// cursor-repositioning to overwrite prior scrollback lines, title-bar
// spoofing, etc). The TUI applies its OWN styling via lipgloss on top of
// already-sanitized input, so a raw incoming escape byte is never
// legitimate content — this sanitizes the INPUT, never the final rendered
// frame (which legitimately contains lipgloss's own escapes).
//
// Ordinary text and newlines/tabs pass through untouched; everything else
// in the C0 (0x00-0x1F, 0x7F) and C1 (0x80-0x9F) control ranges is dropped,
// and a recognized escape sequence (CSI/OSC/simple two-byte) is consumed in
// full so its payload bytes don't leak through as visible garbage either.
func sanitizeTerminalText(s string) string {
	runes := []rune(s)
	var out []rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == 0x1b: // ESC: consume the whole escape sequence
			i = skipEscapeSequence(runes, i)
		case r == '\n' || r == '\t':
			out = append(out, r)
		case r < 0x20 || r == 0x7f:
			// other C0 control / DEL: drop
		case r >= 0x80 && r <= 0x9f:
			// C1 control range (includes the single-byte forms of CSI/OSC
			// some sources emit instead of ESC-prefixed): drop
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// skipEscapeSequence returns the index of the LAST rune consumed by the
// escape sequence starting at runes[start] (== ESC), so the caller's loop
// index lands past it on the next iteration. Handles CSI (ESC '[' ...
// final byte 0x40-0x7E), OSC (ESC ']' ... terminated by BEL or ST == ESC
// '\\'), and falls back to a conservative "ESC + one byte" for anything
// else (simple two-character escapes like ESC 'c') — an unterminated
// sequence at end-of-input consumes to the end rather than looping forever.
func skipEscapeSequence(runes []rune, start int) int {
	if start+1 >= len(runes) {
		return start
	}
	switch runes[start+1] {
	case '[': // CSI
		i := start + 2
		for i < len(runes) {
			c := runes[i]
			if c >= 0x40 && c <= 0x7e {
				return i
			}
			i++
		}
		return len(runes) - 1
	case ']': // OSC
		i := start + 2
		for i < len(runes) {
			if runes[i] == 0x07 {
				return i
			}
			if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '\\' {
				return i + 1
			}
			i++
		}
		return len(runes) - 1
	default:
		return start + 1
	}
}
