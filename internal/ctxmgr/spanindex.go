package ctxmgr

import "strings"

// Span is a resident window inside a key's content — line-numbered,
// 1-indexed, inclusive, matching the §3b marker format
// ("src/a.py L40-88 (decode_frame)"). Label is optional (a symbol name);
// empty for the plain line-window fallback.
type Span struct {
	StartLine int
	EndLine   int
	Label     string
}

// Locus is the trigger's pointer into a file — an edit range, a symbol
// named in a diagnostic, or an error line (§3b "the trigger carries a
// locus"). Exactly one of Line/Symbol is normally set; both may be, in
// which case a real indexer prefers Symbol and the line-window fallback
// uses Line.
type Locus struct {
	Line   int
	Symbol string
}

// SpanIndexer resolves a Locus to a Span within content. codemap's
// deterministic symbol index is the spec's named first real
// implementation (§3b) — out of scope for this unit (a separate,
// unmerged package); LSP/tree-sitter are also out of scope. This unit
// ships the seam plus the always-available LineWindowFallback so partial
// re-admission works with no indexer configured, per §3b "no indexer ->
// line-window fallback around the locus".
type SpanIndexer interface {
	// Resolve returns the span containing locus within content, or false
	// if it cannot (e.g. locus.Symbol not found) — callers fall back to
	// LineWindowFallback on false.
	Resolve(content []byte, locus Locus) (Span, bool)
}

// LineWindowRadius is the default fallback window's half-width in lines
// (total window = 2*LineWindowRadius+1, clamped to the file).
const LineWindowRadius = 20

// LineWindowIndexer is the always-available fallback SpanIndexer: a fixed
// line window centered on locus.Line. It never resolves Symbol (no
// indexer means no symbol table) — Resolve returns false for a
// symbol-only locus with Line == 0.
type LineWindowIndexer struct {
	Radius int
}

// NewLineWindowIndexer builds a fallback indexer; radius<=0 uses
// LineWindowRadius.
func NewLineWindowIndexer(radius int) *LineWindowIndexer {
	if radius <= 0 {
		radius = LineWindowRadius
	}
	return &LineWindowIndexer{Radius: radius}
}

func (idx *LineWindowIndexer) Resolve(content []byte, locus Locus) (Span, bool) {
	if locus.Line <= 0 {
		return Span{}, false
	}
	total := countLines(content)
	if total == 0 {
		return Span{}, false
	}
	start := locus.Line - idx.Radius
	if start < 1 {
		start = 1
	}
	end := locus.Line + idx.Radius
	if end > total {
		end = total
	}
	return Span{StartLine: start, EndLine: end}, true
}

func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	n := strings.Count(string(content), "\n") + 1
	return n
}

// ResolveSpan is the §3b partial-re-admission entry point: files under
// partialThreshold always re-enter whole (returns false — "whole"), and
// above it, idx (nil-safe: falls back to a default LineWindowIndexer) is
// asked to resolve locus.
func ResolveSpan(idx SpanIndexer, content []byte, locus Locus, partialThreshold int) (Span, bool) {
	if len(content) < partialThreshold {
		return Span{}, false
	}
	if idx == nil {
		idx = NewLineWindowIndexer(0)
	}
	span, ok := idx.Resolve(content, locus)
	if !ok {
		// Indexer couldn't resolve (e.g. unknown symbol) — the line-window
		// fallback is the documented last resort even when a real indexer
		// is configured but misses.
		span, ok = NewLineWindowIndexer(0).Resolve(content, locus)
	}
	return span, ok
}
