package tui

import "testing"

func TestDiffCell_Render_Golden(t *testing.T) {
	d := DiffCell{
		Path: "internal/tui/stream.go",
		Lines: []DiffLine{
			{Kind: DiffContext, OldNo: 1, NewNo: 1, Text: "package tui"},
			{Kind: DiffContext, OldNo: 2, NewNo: 2, Text: ""},
			{Kind: DiffDel, OldNo: 3, Text: "func old() {}"},
			{Kind: DiffAdd, NewNo: 3, Text: "func newer() {}"},
			{Kind: DiffAdd, NewNo: 4, Text: "func alsoNew() {}"},
			{Kind: DiffContext, OldNo: 4, NewNo: 5, Text: "// trailing context"},
		},
	}
	assertGolden(t, "diff_cell", d.Render(60, PlainTheme()))
}

func TestDiffCell_Render_HardWrapsLongLines(t *testing.T) {
	d := DiffCell{
		Lines: []DiffLine{
			{Kind: DiffAdd, NewNo: 1, Text: "this is a deliberately long line of content that should wrap across more than one output row at a narrow width"},
		},
	}
	got := d.Render(30, PlainTheme())
	if len(got) < 3 {
		t.Fatalf("got %d lines, want a hard-wrapped diff line spanning multiple rows: %v", len(got), got)
	}
	assertGolden(t, "diff_cell_wrapped", got)
}
