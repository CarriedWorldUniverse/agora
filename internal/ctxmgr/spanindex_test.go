package ctxmgr

import (
	"strings"
	"testing"
)

func linesContent(n int) []byte {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "line"
	}
	return []byte(strings.Join(lines, "\n"))
}

func TestResolveSpan_BelowThreshold_AlwaysWhole(t *testing.T) {
	content := linesContent(5) // tiny file
	span, ok := ResolveSpan(nil, content, Locus{Line: 3}, 4096)
	if ok {
		t.Fatalf("file under partial_threshold must always re-enter whole, got span %+v", span)
	}
}

func TestResolveSpan_LineWindowFallback(t *testing.T) {
	content := linesContent(1000)
	span, ok := ResolveSpan(nil, content, Locus{Line: 500}, 10)
	if !ok {
		t.Fatal("expected a resolved span")
	}
	if span.StartLine != 500-LineWindowRadius || span.EndLine != 500+LineWindowRadius {
		t.Fatalf("span = %+v, want a %d-line-radius window around 500", span, LineWindowRadius)
	}
}

func TestResolveSpan_ClampsToFileBounds(t *testing.T) {
	content := linesContent(30)

	span, ok := ResolveSpan(nil, content, Locus{Line: 2}, 10)
	if !ok {
		t.Fatal("expected a resolved span")
	}
	if span.StartLine != 1 {
		t.Fatalf("start = %d, want clamped to 1 (line 2 - radius %d < 1)", span.StartLine, LineWindowRadius)
	}

	span2, ok := ResolveSpan(nil, content, Locus{Line: 29}, 10)
	if !ok {
		t.Fatal("expected a resolved span")
	}
	if span2.EndLine != 30 {
		t.Fatalf("end = %d, want clamped to file length 30 (line 29 + radius %d > 30)", span2.EndLine, LineWindowRadius)
	}
}

func TestResolveSpan_SymbolOnlyLocusFallsThroughToLineWindowMiss(t *testing.T) {
	content := linesContent(1000)
	// No indexer, and locus has no Line — the line-window fallback can't
	// resolve a symbol-only locus.
	_, ok := ResolveSpan(nil, content, Locus{Symbol: "decode_frame"}, 10)
	if ok {
		t.Fatal("symbol-only locus with no indexer must fail to resolve, not silently guess a window")
	}
}

// stubIndexer is a deterministic SpanIndexer test double — the seam a real
// codemap/LSP indexer would satisfy.
type stubIndexer struct {
	spans map[string]Span
}

func (s stubIndexer) Resolve(content []byte, locus Locus) (Span, bool) {
	sp, ok := s.spans[locus.Symbol]
	return sp, ok
}

func TestResolveSpan_ConfiguredIndexerTakesPriority(t *testing.T) {
	content := linesContent(1000)
	idx := stubIndexer{spans: map[string]Span{
		"decode_frame": {StartLine: 40, EndLine: 88, Label: "decode_frame"},
	}}
	span, ok := ResolveSpan(idx, content, Locus{Symbol: "decode_frame"}, 10)
	if !ok || span.StartLine != 40 || span.EndLine != 88 {
		t.Fatalf("span = %+v, want the indexer's exact span", span)
	}
}

func TestResolveSpan_IndexerMissFallsBackToLineWindow(t *testing.T) {
	content := linesContent(1000)
	idx := stubIndexer{spans: map[string]Span{}} // never resolves
	span, ok := ResolveSpan(idx, content, Locus{Line: 500, Symbol: "unknown_symbol"}, 10)
	if !ok {
		t.Fatal("expected fallback to the line window when the indexer misses")
	}
	if span.StartLine != 500-LineWindowRadius {
		t.Fatalf("span = %+v, want line-window fallback", span)
	}
}
