package tui

import (
	"reflect"
	"testing"
)

func TestStream_NewlineGating_HalfLineNeverCommits(t *testing.T) {
	s := NewStreamState()
	s.Append("hello wor")
	if got := s.Commit(); got != nil {
		t.Fatalf("half-formed line committed: %v", got)
	}
	if got := s.Tail(); got != "hello wor" {
		t.Fatalf("tail = %q, want the whole half-line", got)
	}
	s.Append("ld\n")
	got := s.Commit()
	want := []string{"hello world"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Commit() = %v, want %v", got, want)
	}
	if got := s.Tail(); got != "" {
		t.Fatalf("tail after full-line commit = %q, want empty", got)
	}
}

func TestStream_NewlineGating_MultipleCompleteLinesInOneDelta(t *testing.T) {
	s := NewStreamState()
	s.Append("line one\nline two\nline three partial")
	got := s.Commit()
	want := []string{"line one", "line two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Commit() = %v, want %v", got, want)
	}
	if got := s.Tail(); got != "line three partial" {
		t.Fatalf("tail = %q", got)
	}
}

func TestStream_AppendOnly_RawNeverShrinksOrMutates(t *testing.T) {
	s := NewStreamState()
	deltas := []string{"a", "b\n", "c", "d\n", "e"}
	var want string
	for _, d := range deltas {
		prevRaw := s.Raw()
		s.Append(d)
		if got := s.Raw(); got[:len(prevRaw)] != prevRaw {
			t.Fatalf("raw mutated an already-appended prefix: had %q, now %q", prevRaw, got)
		}
		want += d
		s.Commit()
	}
	if s.Raw() != want {
		t.Fatalf("Raw() = %q, want %q", s.Raw(), want)
	}
}

func TestStream_StableLinesOnlyGrow(t *testing.T) {
	s := NewStreamState()
	var prev []string
	feed := []string{"one\n", "two\nthree\n", "four", "\n"}
	for _, d := range feed {
		s.Append(d)
		s.Commit()
		got := s.StableLines()
		if len(got) < len(prev) {
			t.Fatalf("StableLines shrank: had %v, now %v", prev, got)
		}
		for i := range prev {
			if got[i] != prev[i] {
				t.Fatalf("StableLines rewrote index %d: had %q, now %q", i, prev[i], got[i])
			}
		}
		prev = append([]string(nil), got...)
	}
	want := []string{"one", "two", "three", "four"}
	if !reflect.DeepEqual(s.StableLines(), want) {
		t.Fatalf("final StableLines = %v, want %v", s.StableLines(), want)
	}
}

func TestStream_TableHoldback_HeaderWaitsForDelimiter(t *testing.T) {
	s := NewStreamState()
	s.Append("| a | b |\n")
	// Only the header line is complete so far — cannot yet tell whether a
	// delimiter row follows, so it must NOT commit (would flash a bare
	// header that later turns out to be part of a table).
	if got := s.Commit(); got != nil {
		t.Fatalf("header line committed before delimiter seen: %v", got)
	}
	if s.TableHeld() {
		t.Fatalf("table holdback armed before confirmation")
	}
}

func TestStream_TableHoldback_ConfirmedTableFreezesUntilFinalize(t *testing.T) {
	s := NewStreamState()
	s.Append("prose before\n| a | b |\n|---|---|\n| 1 | 2 |\n")
	got := s.Commit()
	want := []string{"prose before"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Commit() = %v, want %v (only the prose line, table held)", got, want)
	}
	if !s.TableHeld() {
		t.Fatalf("expected table holdback armed")
	}
	// A later row reshaping every column must still be possible — nothing
	// commits while held, even a plain non-table line after it.
	s.Append("| 3 | 4 |\nplain line after\n")
	if got := s.Commit(); got != nil {
		t.Fatalf("Commit() during holdback = %v, want nil", got)
	}
	final := s.Finalize()
	wantFinal := []string{"| a | b |", "|---|---|", "| 1 | 2 |", "| 3 | 4 |", "plain line after"}
	if !reflect.DeepEqual(final, wantFinal) {
		t.Fatalf("Finalize() = %v, want %v", final, wantFinal)
	}
	wantStable := append(append([]string(nil), want...), wantFinal...)
	if !reflect.DeepEqual(s.StableLines(), wantStable) {
		t.Fatalf("StableLines() = %v, want %v", s.StableLines(), wantStable)
	}
}

func TestStream_TableHoldback_HeaderLookingLineWithoutDelimiterCommitsNormally(t *testing.T) {
	s := NewStreamState()
	// "|" appears in prose, but the next line is not a delimiter row — must
	// not wedge holdback forever.
	s.Append("a | b is not a table\nnext line\n")
	got := s.Commit()
	want := []string{"a | b is not a table", "next line"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Commit() = %v, want %v", got, want)
	}
	if s.TableHeld() {
		t.Fatalf("table holdback armed for a non-table")
	}
}

// TestStream_TableHoldback_BarePipeInProseThenDashDividerDoesNotFreeze is
// finding #4 (CONFIRMED regression): a prose line containing a pipe (e.g. a
// backtick-quoted shell pipeline) followed by an unrelated bare "---"
// divider/setext-underline must NOT arm table holdback — before the fix,
// isTableDelimiterRow alone (any dash/colon/pipe/space line) false-positive
// matched "---" and froze the rest of the turn until Finalize.
func TestStream_TableHoldback_BarePipeInProseThenDashDividerDoesNotFreeze(t *testing.T) {
	s := NewStreamState()
	s.Append("ran `ls | grep foo`\n---\nnormal paragraph\nmore\n")
	got := s.Commit()
	want := []string{"ran `ls | grep foo`", "---", "normal paragraph", "more"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Commit() = %v, want %v (must not freeze)", got, want)
	}
	if s.TableHeld() {
		t.Fatalf("table holdback armed for a non-table (bare pipe in prose + unrelated divider)")
	}
}

// TestStream_TableHoldback_GenuineTableStillHoldsBack proves the column-
// count tightening didn't break real table detection: a genuine
// `col|col` / `---|---` pair (matching cell counts) still arms holdback.
func TestStream_TableHoldback_GenuineTableStillHoldsBack(t *testing.T) {
	s := NewStreamState()
	s.Append("col|col\n---|---\n")
	got := s.Commit()
	if got != nil {
		t.Fatalf("Commit() = %v, want nil (table held)", got)
	}
	if !s.TableHeld() {
		t.Fatalf("expected table holdback armed for a genuine table")
	}
}

func TestStream_Finalize_CommitsTrailingIncompleteLine(t *testing.T) {
	s := NewStreamState()
	s.Append("committed\npartial tail no newline")
	s.Commit()
	got := s.Finalize()
	want := []string{"partial tail no newline"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Finalize() = %v, want %v", got, want)
	}
	if !s.Done() {
		t.Fatalf("Done() = false after Finalize")
	}
	if s.Tail() != "" {
		t.Fatalf("Tail() after Finalize = %q, want empty", s.Tail())
	}
}

func TestStream_Finalize_EmptyTailIsNoop(t *testing.T) {
	s := NewStreamState()
	s.Append("one\n")
	s.Commit()
	if got := s.Finalize(); got != nil {
		t.Fatalf("Finalize() on empty tail = %v, want nil", got)
	}
	if !reflect.DeepEqual(s.StableLines(), []string{"one"}) {
		t.Fatalf("StableLines() = %v", s.StableLines())
	}
}

func TestStream_AppendAfterFinalizeIsNoop(t *testing.T) {
	s := NewStreamState()
	s.Append("one\n")
	s.Finalize()
	rawBefore := s.Raw()
	s.Append("more text that must be dropped")
	if s.Raw() != rawBefore {
		t.Fatalf("Append after Finalize mutated Raw(): %q", s.Raw())
	}
}

func TestStream_EmptyLineIsPreserved(t *testing.T) {
	s := NewStreamState()
	s.Append("para one\n\npara two\n")
	got := s.Commit()
	want := []string{"para one", "", "para two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Commit() = %v, want %v", got, want)
	}
}
