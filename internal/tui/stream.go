package tui

import (
	"regexp"
	"strings"
)

// StreamState implements the two-region streaming model (agora-spec-tui.md
// §2): raw delta text accumulates append-only into a single buffer; Commit
// promotes newline-gated, table-holdback-aware complete lines out of the
// mutable tail into the stable region. Once a line is returned by Commit or
// Finalize it is considered PRINTED (the caller hands it to the terminal's
// scrollback via tea.Println-style passthrough, §0) and StreamState never
// re-emits or mutates it — the append-only/stable-only-grows invariants are
// enforced by construction: StableLines only ever grows via append.
//
// Rendering (markdown, syntax) is NOT this type's job — it hands back plain
// text lines; a Cell decides how to turn a committed line (or the live tail)
// into styled output. Keeping the invariant machinery decoupled from
// rendering is what makes "append-only, never rewrite" independently
// testable from "looks right" (the tui spec's own §0 caveat: full
// block-aware markdown re-flow across a commit boundary is the hard part
// real codex spends serious code on and the spec explicitly permits
// skipping — v1 renders each stabilized line independently, never
// retroactively).
type StreamState struct {
	raw         strings.Builder
	stableBytes int
	stableLines []string
	tableHeld   bool
	done        bool
}

// NewStreamState returns an empty, not-yet-finalized stream.
func NewStreamState() *StreamState { return &StreamState{} }

// Append adds a delta to the raw, append-only source buffer. A no-op once
// Finalize has been called (documented terminal state — callers construct a
// fresh StreamState per active cell/turn rather than reusing a finalized
// one).
//
// delta is sanitized (finding #3, security) before it ever lands in raw:
// this is the single entry point every byte of agent-message content flows
// through on its way to the real terminal (both the live Tail() and the
// Commit()/Finalize()-stabilized lines handed to Printer), so it is the one
// place that needs to strip control/escape bytes to keep prompt-injected
// ANSI/OSC out of the operator's scrollback.
func (s *StreamState) Append(delta string) {
	if s.done {
		return
	}
	s.raw.WriteString(sanitizeTerminalText(delta))
}

// Raw returns the entire append-only source seen so far (committed + tail).
func (s *StreamState) Raw() string { return s.raw.String() }

// Tail returns the mutable region: everything not yet committed to the
// stable region (re-rendered in the active cell on every delta, §2).
func (s *StreamState) Tail() string { return s.raw.String()[s.stableBytes:] }

// StableLines returns the committed lines so far, in order. The returned
// slice is only ever grown by later calls — callers may safely retain a
// prefix reference across calls (append-only invariant).
func (s *StreamState) StableLines() []string { return s.stableLines }

// Done reports whether Finalize has run.
func (s *StreamState) Done() bool { return s.done }

// TableHeld reports whether table holdback is currently active (a pipe-table
// header+delimiter has been detected in the pending region and everything
// from the header onward is withheld from commit until Finalize).
func (s *StreamState) TableHeld() bool { return s.tableHeld }

// looksLikeTableHeader is a deliberately permissive (false-positive-safe)
// check: any line containing a pipe defers one line's worth of commit
// latency while we wait to see whether the next line is a delimiter row. On
// its own this line-latency claim ("harmless, one extra tick") does NOT
// hold — the confirmation step below (Commit's caller, gated on
// tableCellCount matching) is what keeps a false positive from actually
// arming holdback and freezing the rest of the turn; looksLikeTableHeader
// alone only decides whether to LOOK at the next line, never whether to
// hold back. Spec: agora-spec-tui.md §2.
func looksLikeTableHeader(line string) bool {
	return strings.Contains(line, "|")
}

// tableDelimRe matches a markdown table delimiter row: pipe-separated cells
// of only dashes, optional leading/trailing colons (alignment markers), and
// whitespace — e.g. "| --- | :---: | ---: |" or "---|---".
var tableDelimRe = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)

func isTableDelimiterRow(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || !strings.Contains(t, "-") {
		return false
	}
	return tableDelimRe.MatchString(line)
}

// tableCellCount counts a markdown table row's cells: trim a single
// leading/trailing "|" (rows may or may not be pipe-bounded), then split on
// "|". Used to disambiguate a REAL table header+delimiter pair from a false
// positive (finding #4): a prose line that happens to contain a pipe (e.g.
// a backtick-quoted "ls | grep foo") followed by an unrelated bare "---"
// divider or setext underline is NOT a table — its "header" and
// "delimiter" cell counts won't match.
func tableCellCount(line string) int {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	return len(strings.Split(t, "|"))
}

// splitComplete splits pending text into newline-terminated complete lines
// and any trailing incomplete (not yet newline-terminated) remainder.
// Newline-gating (§2): only complete lines are ever commit-eligible.
func splitComplete(pending string) (complete []string, incompleteTail string) {
	if pending == "" {
		return nil, ""
	}
	if strings.HasSuffix(pending, "\n") {
		parts := strings.Split(pending, "\n")
		return parts[:len(parts)-1], ""
	}
	parts := strings.Split(pending, "\n")
	return parts[:len(parts)-1], parts[len(parts)-1]
}

// Commit promotes as many newline-gated, table-holdback-clear complete
// lines as are currently eligible from the tail into the stable region, and
// returns exactly the newly stabilized lines (nil if none). v1
// simplification (§2): every eligible complete line commits on the very
// next Commit call — no adaptive batching.
func (s *StreamState) Commit() []string {
	if s.tableHeld || s.done {
		return nil
	}
	pending := s.raw.String()[s.stableBytes:]
	complete, _ := splitComplete(pending)
	if len(complete) == 0 {
		return nil
	}

	k := 0
	for k < len(complete) {
		line := complete[k]
		if looksLikeTableHeader(line) {
			if k+1 < len(complete) {
				// A real table's delimiter row has the SAME cell count as
				// its header row (finding #4) — "contains dashes" alone
				// (isTableDelimiterRow) is not sufficient: a bare "---"
				// divider after an unrelated pipe-containing prose line
				// must not arm holdback.
				if isTableDelimiterRow(complete[k+1]) && tableCellCount(line) == tableCellCount(complete[k+1]) {
					// Confirmed table start at k: freeze the boundary here
					// (a later row can still reshape every column) — hold
					// everything from the header onward until Finalize.
					s.tableHeld = true
					break
				}
				// Not actually a table (no delimiter followed) — the
				// header-looking line commits normally; keep scanning.
				k++
				continue
			}
			// Only one more line of lookahead needed to disambiguate and we
			// don't have it yet — defer commit, wait for more deltas.
			break
		}
		k++
	}
	if k == 0 {
		return nil
	}

	newly := append([]string(nil), complete[:k]...)
	advance := 0
	for _, l := range newly {
		advance += len(l) + 1 // +1 for the newline consumed with it
	}
	s.stableBytes += advance
	s.stableLines = append(s.stableLines, newly...)
	return newly
}

// Finalize commits everything remaining in the tail — including a held-back
// table (a table can only be safely reshaped while the stream is still
// open; at finalize there is no more input to reshape it with) and any
// trailing incomplete line (finalize is the one point where an
// unterminated final line is still committed, since no more deltas are
// coming to complete it) — and marks the stream done. Returns the newly
// stabilized lines from this call only.
func (s *StreamState) Finalize() []string {
	pending := s.raw.String()[s.stableBytes:]
	s.stableBytes = s.raw.Len()
	s.tableHeld = false
	s.done = true
	if pending == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(pending, "\n")
	lines := strings.Split(trimmed, "\n")
	s.stableLines = append(s.stableLines, lines...)
	return lines
}
