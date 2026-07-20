package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
)

// CellKind enumerates the v1 cell kinds (agora-spec-tui.md §1).
type CellKind int

const (
	CellSessionHeader CellKind = iota
	CellUserMessage
	CellAgentMessage
	CellReasoning
	CellExec
	CellDiffCell
	CellApprovalDecision
)

// execTailLines is how many of the most recent output lines an active exec
// cell shows while the command is still running (§1: "capped streaming
// output, e.g. last ~20 lines while active").
const execTailLines = 20

// Cell is one entry in the transcript: either already finalized (printed to
// scrollback and forgotten, §0) or the single mutable active cell being
// re-rendered every delta. Render is pure — given a width it always
// produces the same lines for the same Cell state (no wall-clock, no
// terminal-profile auto-detection inside the type itself — callers supply
// Theme).
type Cell struct {
	Kind CellKind

	// SessionHeader
	AgentID string
	Model   string

	// UserMessage / AgentMessage / Reasoning: Text is the raw source. Agent
	// messages render through glamour (markdown); reasoning renders dim,
	// collapsed to a one-line summary unless Expanded.
	Text     string
	Expanded bool

	// Exec
	Command  string
	ExecDone bool
	ExitCode int
	Output   []string // captured output lines, append-only while active

	// DiffCell
	Diff DiffCell

	// ApprovalDecision
	DecisionLabel string // e.g. "approved once", "denied: <message>"
}

// Render turns the cell into plain styled lines at the given width. Never
// mutates the Cell.
func (c Cell) Render(width int, th Theme) []string {
	// finding #3 (security): Text/Command/Output/DecisionLabel ultimately
	// originate from the LLM or a tool (or, for DecisionLabel, may embed
	// operator-typed deny feedback echoed back) — sanitize before this
	// content reaches a style renderer or the real terminal. Sanitizing the
	// INPUT here (not the final lipgloss-rendered frame) is what keeps this
	// safe without stripping the TUI's own intended styling escapes.
	switch c.Kind {
	case CellSessionHeader:
		return []string{th.Header.Render(fmt.Sprintf("── %s · %s ──", c.AgentID, c.Model))}
	case CellUserMessage:
		return wrapPrefixed(th.Accent.Render("›"), sanitizeTerminalText(c.Text), width)
	case CellAgentMessage:
		return renderMarkdown(sanitizeTerminalText(c.Text), width)
	case CellReasoning:
		text := sanitizeTerminalText(c.Text)
		if !c.Expanded {
			return []string{th.Muted.Render(summarizeReasoning(text))}
		}
		lines := strings.Split(text, "\n")
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = th.Muted.Render(l)
		}
		return out
	case CellExec:
		return renderExec(c, th)
	case CellDiffCell:
		return c.Diff.Render(width, th)
	case CellApprovalDecision:
		return []string{th.Muted.Render("• " + sanitizeTerminalText(c.DecisionLabel))}
	default:
		return nil
	}
}

func summarizeReasoning(text string) string {
	first := strings.SplitN(strings.TrimSpace(text), "\n", 2)[0]
	if first == "" {
		return "(reasoning)"
	}
	return "› " + first
}

func wrapPrefixed(prefix, text string, width int) []string {
	lines := strings.Split(text, "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		if i == 0 {
			out[i] = prefix + " " + l
		} else {
			out[i] = "  " + l
		}
	}
	return out
}

func renderExec(c Cell, th Theme) []string {
	var out []string
	// Status gets a glyph + semantic color so the operator can scan a
	// transcript for failures without reading exit codes: amber "(running)"
	// while in flight, green ✓ on success, red ✗ on failure.
	var status string
	switch {
	case !c.ExecDone:
		status = th.Warning.Render("(running)")
	case c.ExitCode == 0:
		status = th.Success.Render(fmt.Sprintf("(✓ exit %d)", c.ExitCode))
	default:
		status = th.Danger.Render(fmt.Sprintf("(✗ exit %d)", c.ExitCode))
	}
	out = append(out, th.Bold.Render("$ "+sanitizeTerminalText(c.Command))+"  "+status)
	lines := c.Output
	if !c.ExecDone && len(lines) > execTailLines {
		lines = lines[len(lines)-execTailLines:]
	}
	for _, l := range lines {
		out = append(out, "  "+sanitizeTerminalText(l))
	}
	return out
}

// renderMarkdown renders a COMPLETE markdown document (a finalized agent
// message) with glamour's colorless "notty" style so output is
// deterministic across environments — the streaming tail is not rendered
// through this path (see stream.go's doc comment: full block-aware
// markdown re-flow across a not-yet-final commit boundary is out of scope
// for v1; the mutable tail renders as plain text via Cell{Kind:
// CellAgentMessage, Text: tail}.Render only once the message is complete).
func renderMarkdown(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("notty"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return strings.Split(text, "\n")
	}
	out, err := r.Render(text)
	if err != nil {
		return strings.Split(text, "\n")
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
