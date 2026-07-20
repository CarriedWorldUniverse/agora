package tui

import (
	"fmt"
	"strings"
)

// DiffLineKind is the gutter sign for one rendered diff line (§7).
type DiffLineKind int

const (
	DiffContext DiffLineKind = iota
	DiffAdd
	DiffDel
)

// DiffLine is one line of a rendered diff/patch: OldNo/NewNo are the
// right-aligned line numbers (0 = blank, e.g. an added line has no OldNo).
// JSON tags: this is also the wire shape a KindPatch approval payload's
// "lines" field decodes into (subject.go, finding #2) — no producer of
// that event exists yet in this repo, so these are this package's own
// contract for it.
type DiffLine struct {
	Kind  DiffLineKind `json:"kind"`
	OldNo int          `json:"oldNo"`
	NewNo int          `json:"newNo"`
	Text  string       `json:"text"`
}

// DiffCell is the diff/patch cell (§1, §7): appears both in the apply-patch
// approval modal body and as a finalized patch cell in the transcript.
type DiffCell struct {
	Path  string
	Lines []DiffLine
}

// styledGutterSign colors the gutter sign per kind (green add / red del) so
// the eye catches line direction even before the background tint registers.
// PlainTheme strips the color, keeping goldens byte-stable.
func styledGutterSign(k DiffLineKind, th Theme) string {
	switch k {
	case DiffAdd:
		return th.Success.Render("+")
	case DiffDel:
		return th.Danger.Render("-")
	default:
		return " "
	}
}

func lineNoCol(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// Render renders the diff: right-aligned line-number gutters + sign +
// content, hard-wrapping long content lines to width while preserving the
// gutter prefix on the continuation (§7: "Hard-wrap long lines preserving
// style spans" — v1 preserves the STYLE (which background/foreground the
// wrapped remainder gets), not sub-span-level styling within a single
// wrapped content line, which needs a proper span-aware wrapper deferred
// as polish per the spec's own "syntax highlighting ... later" allowance).
func (d DiffCell) Render(width int, th Theme) []string {
	numWidth := 4
	for _, l := range d.Lines {
		if w := len(fmt.Sprintf("%d", l.OldNo)); w > numWidth {
			numWidth = w
		}
		if w := len(fmt.Sprintf("%d", l.NewNo)); w > numWidth {
			numWidth = w
		}
	}
	gutterWidth := numWidth*2 + 4 // old + new + " | " + sign
	contentWidth := width - gutterWidth
	if contentWidth < 10 {
		contentWidth = 10
	}

	var out []string
	if d.Path != "" {
		// finding #3 (security): Path/Text ultimately originate from a
		// patch-approval payload — agent/tool-supplied content — sanitize
		// before it reaches the terminal, same boundary rule as stream.go.
		out = append(out, th.Header.Render(sanitizeTerminalText(d.Path)))
	}
	for _, l := range d.Lines {
		prefix := fmt.Sprintf("%*s %*s %s ", numWidth, lineNoCol(l.OldNo), numWidth, lineNoCol(l.NewNo), styledGutterSign(l.Kind, th))
		style := th.DiffLine
		switch l.Kind {
		case DiffAdd:
			style = th.DiffAdd
		case DiffDel:
			style = th.DiffDel
		}
		for _, chunk := range wrapText(sanitizeTerminalText(l.Text), contentWidth) {
			out = append(out, prefix+style.Render(chunk))
			prefix = strings.Repeat(" ", numWidth*2+3) + styledGutterSign(l.Kind, th) + " "
		}
	}
	return out
}

// wrapText hard-wraps s into chunks of at most width runes (byte-length
// approximation is fine for the ASCII-heavy diff content this renders;
// full grapheme-aware wrapping is out of scope for v1).
func wrapText(s string, width int) []string {
	if width <= 0 || len(s) <= width {
		return []string{s}
	}
	var out []string
	for len(s) > width {
		out = append(out, s[:width])
		s = s[width:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
