// Chat panel state + rendering. Spec §9.2.
//
// chatLine is the in-memory representation of one rendered line in the
// scrollback. The Model owns a ring of these (capped at HistoryDepth)
// and the View renders them in viewport-friendly order. Each line
// carries its class so the renderer can color it consistently.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// chatClass enumerates the message classes spec §9.2 calls out. The
// Model uses these to pick a style and a prefix; nothing else depends
// on the value.
type chatClass int

const (
	classChatIn  chatClass = iota // chat message arriving from the bus
	classChatOut                  // outgoing chat we sent
	classTTYIn                    // operator-typed line (echoed locally)
	classNotify                   // notify_operator tool output
	classSystem                   // connect/disconnect/error banner
	classModel                    // assistant streaming text (NEX-57)
)

// chatLine is one entry in the scrollback. Fields kept tiny —
// rendering recomputes timestamp + prefix on every View, which is
// cheap and avoids stale formatting after a resize.
type chatLine struct {
	class chatClass
	when  time.Time
	from  string
	body  string
}

// appendChatLine pushes onto the ring buffer, trimming the head if
// over the cap. Returns the new slice — the caller assigns it back.
func appendChatLine(buf []chatLine, line chatLine, cap int) []chatLine {
	buf = append(buf, line)
	if len(buf) > cap {
		buf = buf[len(buf)-cap:]
	}
	return buf
}

// renderChatLine produces a single styled string for one line. Format
// is "HH:MM:SS prefix body" — the prefix carries from/class info.
// Long bodies wrap on width via lipgloss; this returns the raw
// non-wrapped string, the caller wraps with lipgloss.Width.
func renderChatLine(l chatLine) string {
	ts := dimStyle.Render(l.when.Local().Format("15:04:05"))
	prefix, body := stylePrefixBody(l)
	return fmt.Sprintf("%s %s %s", ts, prefix, body)
}

// stylePrefixBody returns the styled (prefix, body) pair for a line.
// Pulled out so View can reuse it without re-allocating styles per
// call.
func stylePrefixBody(l chatLine) (string, string) {
	switch l.class {
	case classChatIn:
		return chatInStyle.Render(l.from + ":"), l.body
	case classChatOut:
		return chatOutStyle.Render("you →"), l.body
	case classTTYIn:
		return ttyStyle.Render("you:"), l.body
	case classNotify:
		return notifyStyle.Render("notify:"), notifyBodyStyle.Render(l.body)
	case classSystem:
		return systemStyle.Render("·"), systemStyle.Render(l.body)
	case classModel:
		return modelStyle.Render(l.from + ":"), l.body
	default:
		return l.from + ":", l.body
	}
}

var (
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	chatInStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC")).Bold(true)
	chatOutStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	ttyStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")).Bold(true)
	notifyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6")).Bold(true)
	notifyBodyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6"))
	systemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	modelStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Bold(true)
	headerStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1E90FF"))
	dividerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

// wrapLines applies width-based wrapping to a multiline string. Used
// to keep long messages visible without horizontal scroll. Empty
// strings return a single empty line so blank entries don't disappear.
func wrapLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

// renderChatBuffer joins all visible lines into the chat panel body
// given a target width + height. Lines past the bottom of the visible
// area (height) get clipped from the *top* so the most recent line is
// always anchored at the bottom — matching how every chat client
// behaves.
func renderChatBuffer(lines []chatLine, width, height int) string {
	if height <= 0 {
		return ""
	}
	rendered := make([]string, 0, len(lines))
	for _, l := range lines {
		rendered = append(rendered, wrapLines(renderChatLine(l), width))
	}
	joined := strings.Join(rendered, "\n")

	// Anchor to the bottom: count the number of visible lines after
	// wrapping and trim the top if we overflow.
	visible := strings.Split(joined, "\n")
	if len(visible) > height {
		visible = visible[len(visible)-height:]
	} else if len(visible) < height {
		// Pad top so the chat stays anchored to the bottom even when
		// scrollback is short.
		pad := make([]string, height-len(visible))
		visible = append(pad, visible...)
	}
	return strings.Join(visible, "\n")
}
