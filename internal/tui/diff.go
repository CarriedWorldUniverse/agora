package tui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

// --- /diff pager (§7: "Appears in the apply-patch approval modal AND in
// finalized patch cells" — this is the third caller, running `git diff` and
// printing the result through the same DiffCell.Render the approval modal
// already uses, so the pager and the modal always look identical). ---

// diffArgsDisallowed rejects an operator-typed /diff argument string outright
// rather than trying to shell-escape it: this runs a real subprocess (§7's
// "git diff pager"), so anything that could mean something to a shell if the
// string were ever mis-handled downstream (it isn't — exec.CommandContext
// never invokes a shell — but a locally-typed arg string is still untrusted
// enough to bound defensively, and rejecting outright is simpler to audit
// than proving argv-splitting is always safe) is refused up front with a
// local error, never executed. This is deliberately conservative: it isn't a
// full flag grammar for git-diff, just a byte-level blocklist per the spec.
var diffArgsDisallowed = regexp.MustCompile("[;|&$><`\n\r]")

// runSlashDiff runs `git diff [args]` in the process cwd and renders it
// through DiffCell.Render (§7). Never sends anything to the model — like
// every command in this table, it's a local client action.
func runSlashDiff(m *Model, args string) tea.Cmd {
	if diffArgsDisallowed.MatchString(args) {
		return m.cfg.Printer("unsupported /diff argument")
	}
	argv := []string{"diff"}
	if args != "" {
		argv = append(argv, strings.Fields(args)...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = cwdOrDot()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return m.cfg.Printer("git diff timed out")
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(strings.ToLower(msg), "not a git repository") {
			return m.cfg.Printer("not a git repository")
		}
		return m.cfg.Printer("git diff failed: " + firstLine(msg))
	}
	if strings.TrimSpace(string(out)) == "" {
		return m.cfg.Printer("no changes")
	}
	return m.cfg.Printer(renderDiffOutput(string(out), m.width, m.cfg.Theme))
}

// firstLine returns s up to its first newline (git's stderr is often
// multi-line; the pager's error line should stay one line).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var (
	diffGitHeaderRe = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)
	diffHunkRe      = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

// renderDiffOutput renders raw `git diff` stdout for the pager. It parses
// the output into per-file DiffCells (reusing DiffCell.Render — the exact
// path the apply-patch approval modal already renders through, so /diff and
// the modal look identical) and falls back to plain sanitized monospace text
// when the parse can't place a single file (parseUnifiedDiff found no
// "diff --git" header at all): DiffCell.Render needs real old/new line
// numbers, which only a well-formed unified diff carries — a binary-only
// diff, a diff produced with an unexpected --format, or output this parser
// simply doesn't recognize yet all degrade to the plain path instead of
// silently dropping content or crashing on a malformed structure.
func renderDiffOutput(raw string, width int, th Theme) string {
	cells, ok := parseUnifiedDiff(raw)
	if !ok {
		var lines []string
		for _, l := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
			lines = append(lines, th.DiffLine.Render(sanitizeTerminalText(l)))
		}
		return strings.Join(lines, "\n")
	}
	var out []string
	for i, c := range cells {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, c.Render(width, th)...)
	}
	return strings.Join(out, "\n")
}

// parseUnifiedDiff turns `git diff`'s stdout into one DiffCell per file. ok
// is false when the input contains no recognizable "diff --git" header at
// all (renderDiffOutput's cue to fall back to plain text) — anything else
// unrecognized WITHIN a file section (extended headers like "index ..",
// "new file mode ..", "Binary files .. differ", rename/copy markers) is
// simply skipped rather than aborting the whole parse, since those are
// well-known, harmless-to-drop lines that carry no line-numbered content.
func parseUnifiedDiff(raw string) (cells []DiffCell, ok bool) {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	var cur *DiffCell
	oldNo, newNo := 0, 0
	flush := func() {
		if cur != nil {
			cells = append(cells, *cur)
			cur = nil
		}
	}
	for _, l := range lines {
		if m := diffGitHeaderRe.FindStringSubmatch(l); m != nil {
			flush()
			cur = &DiffCell{Path: m[2]}
			oldNo, newNo = 0, 0
			ok = true
			continue
		}
		if cur == nil {
			// Content before the first "diff --git" (e.g. a stray leading
			// blank line) — nothing to attach it to yet.
			continue
		}
		if m := diffHunkRe.FindStringSubmatch(l); m != nil {
			oldNo, _ = strconv.Atoi(m[1])
			newNo, _ = strconv.Atoi(m[3])
			continue
		}
		switch {
		case strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "--- "),
			strings.HasPrefix(l, "index "), strings.HasPrefix(l, "new file mode"),
			strings.HasPrefix(l, "deleted file mode"), strings.HasPrefix(l, "old mode"),
			strings.HasPrefix(l, "new mode"), strings.HasPrefix(l, "similarity index"),
			strings.HasPrefix(l, "rename from"), strings.HasPrefix(l, "rename to"),
			strings.HasPrefix(l, "copy from"), strings.HasPrefix(l, "copy to"),
			strings.HasPrefix(l, `\ No newline`):
			// Extended-header/no-hunk lines: carry no line-numbered content.
		case strings.HasPrefix(l, "Binary files ") && strings.HasSuffix(l, " differ"):
			cur.Lines = append(cur.Lines, DiffLine{Kind: DiffContext, Text: l})
		case oldNo == 0 && newNo == 0:
			// Between the file header and the first hunk, with none of the
			// recognized extended-header prefixes above: not a shape this
			// parser knows.
		case strings.HasPrefix(l, "+"):
			cur.Lines = append(cur.Lines, DiffLine{Kind: DiffAdd, NewNo: newNo, Text: l[1:]})
			newNo++
		case strings.HasPrefix(l, "-"):
			cur.Lines = append(cur.Lines, DiffLine{Kind: DiffDel, OldNo: oldNo, Text: l[1:]})
			oldNo++
		case strings.HasPrefix(l, " "):
			cur.Lines = append(cur.Lines, DiffLine{Kind: DiffContext, OldNo: oldNo, NewNo: newNo, Text: l[1:]})
			oldNo++
			newNo++
		case l == "":
			cur.Lines = append(cur.Lines, DiffLine{Kind: DiffContext, OldNo: oldNo, NewNo: newNo, Text: ""})
			oldNo++
			newNo++
		}
	}
	flush()
	return cells, ok
}
