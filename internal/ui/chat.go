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

	// operatorRelevant flags lines that touch the operator directly:
	// operator-authored (tty echoes, outgoing chat), addressed to the
	// operator (@-mention in body), or system/notify lines. The view's
	// filter mode hides non-operator-relevant lines by default — intra-
	// aspect coordination that doesn't include the operator folds out
	// of sight, restorable via a hotkey. NEX-118.
	operatorRelevant bool
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

// renderStreamingLine renders the live-line preview from the raw
// stream buffer, masking content inside an as-yet-unclosed code
// fence. Spec §10: code blocks buffer until close fence so partial
// `\`\`\`go` + half-rendered lines don't flicker into view before
// the whole block is available.
//
// Algorithm: walk the buffer counting `\`\`\`` toggles. If the count
// is even (zero or all-paired), the whole buffer is visible. If odd
// (an open fence is in progress), show everything UP TO the open
// fence + a small placeholder.
func renderStreamingLine(buf string) string {
	fence := "```"
	openIdx := -1
	open := false
	i := 0
	for i+len(fence) <= len(buf) {
		if buf[i:i+len(fence)] == fence {
			open = !open
			if open {
				openIdx = i
			} else {
				openIdx = -1
			}
			i += len(fence)
			continue
		}
		i++
	}
	if open && openIdx >= 0 {
		return buf[:openIdx] + dimStyle.Render("[code block streaming…]")
	}
	return buf
}

// renderChatContent renders the full chat scrollback as a single
// string, wrapped to width but NOT height-clipped — the caller
// (bubbles/viewport) owns the scroll region and clipping. Spec §11.
//
// NEX-118: when filterChatter is true, lines without operatorRelevant
// are skipped. A trailing summary line surfaces the hidden count so
// the operator knows context exists.
func renderChatContent(lines []chatLine, width int, filterChatter bool) string {
	rendered := make([]string, 0, len(lines))
	hidden := 0
	for _, l := range lines {
		if filterChatter && !l.operatorRelevant {
			hidden++
			continue
		}
		rendered = append(rendered, wrapLines(renderChatLine(l), width))
	}
	if filterChatter && hidden > 0 {
		summary := dimStyle.Render(fmt.Sprintf("… %d background message(s) hidden — Ctrl-T to show all", hidden))
		rendered = append(rendered, wrapLines(summary, width))
	}
	return strings.Join(rendered, "\n")
}

// markOperatorRelevant computes whether a chatLine touches the operator.
// Used at line-creation sites so each line carries the boolean without
// the renderer needing to know the operator's name. NEX-118/NEX-248.
//
// Operator-facing surface = panel-route only by default. Network chat
// (classChatIn, classChatOut) stays hidden unless it directly mentions
// the operator handle. Reasoning: with native broker delivery, peer
// chat fires as turns into the aspect's context, and the operator
// sees the whole back-and-forth interleaved with their conversation.
// Default-hiding network chat keeps the panel = "me and you" until
// the operator pulls network state in on demand.
func markOperatorRelevant(class chatClass, from, body, operatorName string) bool {
	switch class {
	case classTTYIn, classNotify, classSystem, classModel:
		// Operator-authored input, panel-route reply, notify-operator,
		// and system banners always surface — the operator drove or
		// needs to see them.
		return true
	}
	// classChatIn / classChatOut: incoming or outgoing bus traffic.
	// Relevant only if operator is in the From or the body @-mentions
	// the operator handle. Otherwise the line stays hidden behind the
	// filter; aspect-to-aspect coordination doesn't paint by default.
	if from == operatorName {
		return true
	}
	if operatorName != "" && strings.Contains(body, "@"+operatorName) {
		return true
	}
	return false
}
