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

// blockClass tags each chatBlock. Drives header colour, body colour,
// and optional state suffix.
type blockClass int

const (
	blockYou            blockClass = iota // operator-typed input echo
	blockAspect                           // aspect reply (panel OR chat mirror)
	blockAspectThinking                   // active streaming; ends at TurnDone
	blockNotify                           // notify-operator content
	blockSystem                           // dropped submission, error, banner
	blockDivider                          // since-you-left, session start
)

// chatBlock is the post-rewrite scrollback primitive. One block per
// turn worth of output; body mutates in place during streaming.
type chatBlock struct {
	class     blockClass
	speaker   string
	body      strings.Builder
	createdAt time.Time
	failed    bool
	failedMsg string // populated when failed=true; renders in header
}

// renderChatBlock produces the styled header rule line + indented body.
// showTS adds "HH:MM" to the right edge of the header rule.
func renderChatBlock(b chatBlock, width int, showTS bool) string {
	headerText := blockHeaderText(b)
	headerStyleFn := blockHeaderStyle(b)

	tsSuffix := ""
	if showTS {
		tsSuffix = " " + dimStyle.Render(b.createdAt.Format("15:04"))
	}

	// header: "<glyph?><speaker><state?> <rule fill> <ts?>"
	leftWidth := lipgloss.Width(headerText)
	rightWidth := lipgloss.Width(tsSuffix)
	ruleWidth := width - leftWidth - rightWidth - 1 // 1 for space between
	if ruleWidth < 3 {
		ruleWidth = 3
	}
	header := headerStyleFn.Render(headerText) + " " + dividerStyle.Render(strings.Repeat("─", ruleWidth)) + tsSuffix

	body := b.body.String()
	if body == "" {
		return header
	}
	bodyStyleFn := blockBodyStyle(b)
	wrapped := wrapLines(body, width-2)
	indented := indentLines(wrapped, "  ")
	return header + "\n" + bodyStyleFn.Render(indented)
}

func blockHeaderText(b chatBlock) string {
	switch b.class {
	case blockYou:
		return "you"
	case blockAspect:
		s := b.speaker
		if b.failed {
			s += " · failed: " + b.failedMsg
		}
		return s
	case blockAspectThinking:
		return b.speaker + " · thinking"
	case blockNotify:
		return "⚡ " + b.speaker
	case blockSystem:
		return "· system"
	case blockDivider:
		return "─── " + b.body.String() + " "
	default:
		return b.speaker
	}
}

func blockHeaderStyle(b chatBlock) lipgloss.Style {
	switch b.class {
	case blockYou:
		return ttyStyle
	case blockAspect:
		if b.failed {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171")).Bold(true)
		}
		return modelStyle
	case blockAspectThinking:
		return modelStyle.Italic(true)
	case blockNotify:
		return notifyStyle
	case blockSystem:
		return systemStyle
	case blockDivider:
		return dimStyle
	}
	return modelStyle
}

func blockBodyStyle(b chatBlock) lipgloss.Style {
	switch b.class {
	case blockNotify:
		return notifyBodyStyle
	case blockSystem:
		return systemStyle
	}
	return lipgloss.NewStyle()
}

func indentLines(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// appendClonedBlock appends a fresh chatBlock to out, copying metadata
// and body content from src without ever copying a non-zero
// strings.Builder by value (which would panic at the next write).
func appendClonedBlock(out []*chatBlock, src chatBlock) []*chatBlock {
	b := &chatBlock{
		class:     src.class,
		speaker:   src.speaker,
		createdAt: src.createdAt,
		failed:    src.failed,
		failedMsg: src.failedMsg,
	}
	b.body.WriteString(src.body.String())
	return append(out, b)
}

// coalesceBlocks folds consecutive same-speaker + same-class blocks
// into one (joining bodies with a blank line). Storage stays raw; this
// runs at render time. Divider blocks are never coalesced. Blocks
// with createdAt deltas > 60s also stay separate so distinct events
// remain visible.
func coalesceBlocks(blocks []chatBlock) []chatBlock {
	if len(blocks) == 0 {
		return blocks
	}
	ptrs := make([]*chatBlock, 0, len(blocks))
	ptrs = appendClonedBlock(ptrs, blocks[0])
	for i := 1; i < len(blocks); i++ {
		cur := blocks[i]
		last := ptrs[len(ptrs)-1]
		if last.class != cur.class || last.class == blockDivider || last.speaker != cur.speaker {
			ptrs = appendClonedBlock(ptrs, cur)
			continue
		}
		if cur.createdAt.Sub(last.createdAt) > 60*time.Second {
			ptrs = appendClonedBlock(ptrs, cur)
			continue
		}
		last.body.WriteString("\n\n")
		last.body.WriteString(cur.body.String())
	}
	out := make([]chatBlock, len(ptrs))
	for i, p := range ptrs {
		out[i] = *p
	}
	return out
}

// renderBlockContent renders a slice of blocks at the given width,
// returns one string suitable for viewport.SetContent. Mirrors
// renderChatContent's signature but for blocks.
func renderBlockContent(blocks []chatBlock, width int, showTS bool) string {
	coalesced := coalesceBlocks(blocks)
	parts := make([]string, 0, len(coalesced))
	for _, b := range coalesced {
		parts = append(parts, renderChatBlock(b, width, showTS))
	}
	return strings.Join(parts, "\n\n")
}
