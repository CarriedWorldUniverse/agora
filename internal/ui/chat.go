// Block-based chat scrollback: types, rendering, fold/coalesce.
// Style declarations live in styles.go. Model-side block lifecycle
// (append, mutate, divider tracking) lives in blocks.go.
package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
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
// Algorithm: walk the buffer counting `\`\`\“ toggles. If the count
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

// blockClass tags each chatBlock. Drives header colour, body colour,
// and optional state suffix.
type blockClass int

const (
	blockYou     blockClass = iota // operator-typed input echo
	blockAspect                    // agent reply
	blockNotify                    // operator notification content
	blockSystem                    // error or banner
	blockDivider                   // since-you-left, session start
)

// chatBlock is the scrollback primitive.
type chatBlock struct {
	class     blockClass
	speaker   string
	body      strings.Builder
	createdAt time.Time
	failed    bool
	failedMsg string // populated when failed=true; renders in header
	msgID     int64
	pending   bool // sent, awaiting broker echo — renders "…"
	delivered bool // echo reconciled — renders "✓"
}

// sendStateMarker is the dim delivery-state suffix on operator blocks:
// "…" while awaiting the broker echo, "✓" once the echo reconciles,
// "✗ undelivered" on RPC failure or ack timeout.
func sendStateMarker(b chatBlock) string {
	if b.class != blockYou {
		return ""
	}
	switch {
	case b.pending:
		return "…"
	case b.failed:
		return "✗ undelivered"
	case b.delivered:
		return "✓"
	}
	return ""
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
	left := headerStyleFn.Render(headerText)
	if marker := sendStateMarker(b); marker != "" {
		left += " " + dimStyle.Render(marker)
	}
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(tsSuffix)
	ruleWidth := width - leftWidth - rightWidth - 1 // 1 for space between
	if ruleWidth < 3 {
		ruleWidth = 3
	}
	header := left + " " + dividerStyle.Render(strings.Repeat("─", ruleWidth)) + tsSuffix

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
		// Delivery state renders as a dim marker suffix
		// (sendStateMarker), not in the header text.
		return "you"
	case blockAspect:
		s := b.speaker
		if b.failed {
			s += " · failed: " + b.failedMsg
		}
		return s
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
		pending:   src.pending,
		delivered: src.delivered,
	}
	b.body.WriteString(src.body.String())
	return append(out, b)
}

// coalesceBlocks folds consecutive same-speaker + same-class blocks
// into one (joining bodies with a blank line). Storage stays raw; this
// runs at render time. Divider blocks are never coalesced. Blocks
// with createdAt deltas > 60s also stay separate so distinct events
// remain visible.
// coalesceBlocks merges consecutive same-speaker / same-class blocks
// into a single rendered block. Returns a fresh []*chatBlock so the
// caller can range over pointers without risk of Builder copies
// re-tripping the copyCheck.
func coalesceBlocks(blocks []*chatBlock) []*chatBlock {
	if len(blocks) == 0 {
		return nil
	}
	ptrs := make([]*chatBlock, 0, len(blocks))
	ptrs = appendClonedBlock(ptrs, *blocks[0])
	for i := 1; i < len(blocks); i++ {
		cur := blocks[i]
		last := ptrs[len(ptrs)-1]
		if last.class != cur.class || last.class == blockDivider || last.speaker != cur.speaker {
			ptrs = appendClonedBlock(ptrs, *cur)
			continue
		}
		// Blocks in different delivery states keep their own markers —
		// a pending send must not fold into a delivered/failed one.
		if last.pending != cur.pending || last.delivered != cur.delivered || last.failed != cur.failed {
			ptrs = appendClonedBlock(ptrs, *cur)
			continue
		}
		if cur.createdAt.Sub(last.createdAt) > 60*time.Second {
			ptrs = appendClonedBlock(ptrs, *cur)
			continue
		}
		last.body.WriteString("\n\n")
		last.body.WriteString(cur.body.String())
	}
	return ptrs
}

// renderBlockContent renders a slice of blocks at the given width,
// returns one string suitable for viewport.SetContent. Mirrors
// renderChatContent's signature but for blocks.
func renderBlockContent(blocks []*chatBlock, width int, showTS bool) string {
	coalesced := coalesceBlocks(blocks)
	parts := make([]string, 0, len(coalesced))
	for _, b := range coalesced {
		// Dereference for the value-param renderer. Render path is
		// read-only — calls b.body.String(), never Write — so the
		// implicit Builder copy in the call is safe.
		parts = append(parts, renderChatBlock(*b, width, showTS))
	}
	return strings.Join(parts, "\n\n")
}
