package tui

import (
	"encoding/json"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// This file decodes and renders the "subject" of a permission-shaped
// approval — the concrete command/diff/tool being approved — into the
// modal body (agora-spec-tui.md §3: "Body shows the highlighted command or
// the diff"). Before this existed, renderModal's default case rendered
// only "approve <kind>?" + option labels: the operator had no way to see
// WHAT they were approving before choosing allow/deny, which is the core
// trust function of the gate (finding #2).
//
// Wire shapes below are this package's own contract for e.Raw (the
// approval_requested event's kind-specific sub-payload) — no producer of
// these events exists yet in this repo (internal/daemon/U18 is unbuilt),
// so there was no prior established shape to match; these are deliberately
// simple, structured JSON so the TUI never has to parse a unified diff or
// shell-quote a command line itself.

// subjectMaxLen bounds a single rendered text subject (command/tool/args/
// escalation detail) so a huge payload can't blow out the modal.
const subjectMaxLen = 200

// subjectMaxDiffLines bounds how many diff lines the patch modal renders.
const subjectMaxDiffLines = 40

// execSubjectPayload is the e.Raw shape for KindExec and KindGate: the
// literal command line being requested.
type execSubjectPayload struct {
	Command string `json:"command"`
}

// patchSubjectPayload is the e.Raw shape for KindPatch: the file path plus
// pre-computed diff lines (DiffCell's own shape) — the diff-rendering
// engine's job is producing this on the daemon side; the TUI only renders
// it (DiffCell.Render, wired here rather than re-implemented).
type patchSubjectPayload struct {
	Path  string     `json:"path"`
	Lines []DiffLine `json:"lines"`
}

// mcpToolSubjectPayload is the e.Raw shape for KindMCPTool.
type mcpToolSubjectPayload struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args,omitempty"`
}

// escalationSubjectPayload is the e.Raw shape for KindEscalation.
type escalationSubjectPayload struct {
	Detail string `json:"detail"`
}

// renderApprovalSubject decodes e.Raw per e.Kind and returns the styled
// lines to show above the modal's options — nil if there is no subject to
// show (decode failure, or the payload genuinely carries nothing — e.g.
// the nil-payload tests elsewhere in this package that only exercise the
// option list). Never dereferences a typed pointer, so a malformed e.Raw
// degrades to "no subject shown" rather than a crash (kept independent of
// finding #1's queue-time guard, which is about Question/Plan specifically).
func renderApprovalSubject(e approvalEntry, th Theme, width int) []string {
	// Content here ultimately originates from the agent/tool side of the
	// wire, so it flows through sanitizeTerminalText (finding #3) before
	// reaching lipgloss/the terminal — the patch case delegates that to
	// DiffCell.Render, which sanitizes internally.
	switch e.Kind {
	case contracts.KindExec, contracts.KindGate:
		var p execSubjectPayload
		if json.Unmarshal(e.Raw, &p) != nil || p.Command == "" {
			return nil
		}
		return []string{th.Bold.Render("$ " + truncateSubject(sanitizeTerminalText(p.Command)))}
	case contracts.KindPatch:
		var p patchSubjectPayload
		if json.Unmarshal(e.Raw, &p) != nil || len(p.Lines) == 0 {
			return nil
		}
		lines := p.Lines
		if len(lines) > subjectMaxDiffLines {
			lines = lines[:subjectMaxDiffLines]
		}
		d := DiffCell{Path: p.Path, Lines: lines}
		return d.Render(width, th)
	case contracts.KindMCPTool:
		var p mcpToolSubjectPayload
		if json.Unmarshal(e.Raw, &p) != nil || p.Tool == "" {
			return nil
		}
		line := "tool: " + sanitizeTerminalText(p.Tool)
		if len(p.Args) > 0 && string(p.Args) != "null" {
			line += "  args: " + truncateSubject(sanitizeTerminalText(string(p.Args)))
		}
		return []string{th.Bold.Render(line)}
	case contracts.KindEscalation:
		var p escalationSubjectPayload
		if json.Unmarshal(e.Raw, &p) != nil || p.Detail == "" {
			return nil
		}
		return []string{th.Bold.Render(truncateSubject(sanitizeTerminalText(p.Detail)))}
	default:
		return nil
	}
}

func truncateSubject(s string) string {
	r := []rune(s)
	if len(r) <= subjectMaxLen {
		return s
	}
	return string(r[:subjectMaxLen]) + "…(truncated)"
}
