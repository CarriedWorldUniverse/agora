package memory

import (
	"strings"
	"testing"
	"time"
)

func mkEntries(n int) []IndexEntry {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	out := make([]IndexEntry, n)
	// Index 0 is "newest" (caller-supplied order is assumed already
	// newest-first, matching Store.List's contract).
	for i := 0; i < n; i++ {
		out[i] = IndexEntry{
			Slug:    "slug" + itoa(i),
			Title:   "Title " + itoa(i),
			Hook:    "hook for entry " + itoa(i),
			Type:    TypeReference,
			ModTime: base.Add(time.Duration(n-i) * time.Hour),
		}
	}
	return out
}

func itoa(i int) string {
	// Avoid importing strconv purely for test scaffolding; small range only.
	digits := "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return string(b)
}

func TestRenderIndexEmptyStore(t *testing.T) {
	frag := RenderIndex(nil, Budget(0))
	if !strings.Contains(frag.Text, "<memory_index>") || !strings.Contains(frag.Text, "</memory_index>") {
		t.Fatalf("empty RenderIndex missing wrapper tags: %q", frag.Text)
	}
	if !strings.Contains(frag.Text, "no saved memories") {
		t.Fatalf("empty RenderIndex should say so: %q", frag.Text)
	}
	if len(frag.Warnings) != 0 {
		t.Fatalf("empty RenderIndex warnings = %v, want none", frag.Warnings)
	}
}

func TestRenderIndexFitsEverythingUnderGenerousBudget(t *testing.T) {
	entries := mkEntries(5)
	frag := RenderIndex(entries, 100000)
	if len(frag.Warnings) != 0 {
		t.Fatalf("generous budget produced warnings: %v", frag.Warnings)
	}
	for _, e := range entries {
		if !strings.Contains(frag.Text, e.File()) {
			t.Errorf("fragment missing entry %s: %q", e.Slug, frag.Text)
		}
	}
}

func TestRenderIndexTruncatesWholeLinesNewestFirstSurvives(t *testing.T) {
	entries := mkEntries(20)
	// A tight budget that fits some but not all lines.
	full := RenderIndex(entries, 100000)
	tight := len(full.Text) / 3
	frag := RenderIndex(entries, tight)

	if len(frag.Warnings) == 0 {
		t.Fatalf("tight budget produced no warnings, want an omission warning")
	}
	// The newest entry (index 0) must survive; some tail (oldest) entries
	// must be omitted.
	if !strings.Contains(frag.Text, entries[0].File()) {
		t.Fatalf("newest entry omitted under tight budget: %q", frag.Text)
	}
	if strings.Contains(frag.Text, entries[len(entries)-1].File()) {
		t.Fatalf("oldest entry survived a tight budget that should have dropped it: %q", frag.Text)
	}
	// No line may be PARTIALLY rendered — every included line must be a
	// complete "- [Title](file) — hook" line ending in the entry's hook.
	for _, line := range strings.Split(strings.TrimRight(frag.Text, "\n"), "\n") {
		if !strings.HasPrefix(line, "- [") {
			continue
		}
		if !strings.Contains(line, "](slug") {
			t.Errorf("malformed/partial index line: %q", line)
		}
	}
	if len(frag.Text) > tight {
		t.Fatalf("rendered fragment (%d bytes) exceeds budget (%d bytes)", len(frag.Text), tight)
	}
}

func TestRenderIndexBudgetTooSmallForShell(t *testing.T) {
	entries := mkEntries(3)
	frag := RenderIndex(entries, 4) // smaller than even the wrapper tags
	if frag.Text != "" {
		t.Fatalf("Text = %q, want empty when budget too small for the shell", frag.Text)
	}
	if len(frag.Warnings) == 0 {
		t.Fatalf("want a warning when budget too small for the shell")
	}
}

func TestBudgetFallbackAndFormula(t *testing.T) {
	if got := Budget(0); got != FallbackBudgetChars {
		t.Fatalf("Budget(0) = %d, want fallback %d", got, FallbackBudgetChars)
	}
	if got := Budget(-5); got != FallbackBudgetChars {
		t.Fatalf("Budget(-5) = %d, want fallback %d", got, FallbackBudgetChars)
	}
	// 100000 tokens * 2% = 2000 tokens * 4 bytes/token = 8000 bytes.
	if got := Budget(100000); got != 8000 {
		t.Fatalf("Budget(100000) = %d, want 8000", got)
	}
}

func TestIndexLineFormat(t *testing.T) {
	e := IndexEntry{Slug: "op", Title: "Operator", Hook: "who runs shadow"}
	got := indexLine(e)
	want := "- [Operator](op.md) — who runs shadow\n"
	if got != want {
		t.Fatalf("indexLine = %q, want %q", got, want)
	}
}

func TestIndexLineFormatNoHook(t *testing.T) {
	e := IndexEntry{Slug: "op", Title: "Operator"}
	got := indexLine(e)
	want := "- [Operator](op.md)\n"
	if got != want {
		t.Fatalf("indexLine (no hook) = %q, want %q", got, want)
	}
}

func TestStoreRenderIndexIntegration(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "a", "A", "hook a", TypeUser, time.Time{})
	mustWrite(t, s, "b", "B", "hook b", TypeUser, time.Time{})

	frag, err := s.RenderIndex(100000)
	if err != nil {
		t.Fatalf("Store.RenderIndex: %v", err)
	}
	if !strings.Contains(frag.Text, "a.md") || !strings.Contains(frag.Text, "b.md") {
		t.Fatalf("Store.RenderIndex missing entries: %q", frag.Text)
	}
}
