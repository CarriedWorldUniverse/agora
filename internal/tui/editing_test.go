package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestComposer_CursorMidlineInsert: Home + MoveRight let you insert in the
// middle of the buffer, not just at the end.
func TestComposer_CursorMidlineInsert(t *testing.T) {
	c := NewComposer()
	c.InsertText("helloworld")
	c.Home()
	if c.Cursor() != 0 {
		t.Fatalf("Home: cursor=%d want 0", c.Cursor())
	}
	for i := 0; i < 5; i++ {
		c.MoveRight()
	}
	c.InsertText(" ")
	if c.Value() != "hello world" {
		t.Fatalf("mid-line insert: %q want %q", c.Value(), "hello world")
	}
	c.End()
	if c.Cursor() != len([]rune("hello world")) {
		t.Fatalf("End: cursor=%d want %d", c.Cursor(), len([]rune("hello world")))
	}
}

// TestComposer_DeleteForward: Delete removes the rune AT the cursor; MoveLeft
// then Delete removes the char to the right.
func TestComposer_DeleteForward(t *testing.T) {
	c := NewComposer()
	c.InsertText("abcd")
	c.Home()
	c.Delete() // removes 'a'
	if c.Value() != "bcd" {
		t.Fatalf("Delete at 0: %q want %q", c.Value(), "bcd")
	}
	c.End()
	c.Delete() // no-op at end
	if c.Value() != "bcd" {
		t.Fatalf("Delete at end changed value: %q", c.Value())
	}
	c.MoveLeft() // before 'd'
	c.Delete()   // removes 'd'
	if c.Value() != "bc" {
		t.Fatalf("Delete before last: %q want %q", c.Value(), "bc")
	}
}

// TestHandleKey_Multiline: alt+enter / ctrl+j insert a newline (multi-line
// input) while plain Enter still submits.
func TestHandleKey_Multiline(t *testing.T) {
	m := testModel(newFakeBackend())
	m.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line1")})
	m.press(tea.KeyMsg{Type: tea.KeyEnter, Alt: true}) // alt+enter -> newline
	m.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line2")})
	if got := m.composer.Value(); got != "line1\nline2" {
		t.Fatalf("alt+enter newline: %q want %q", got, "line1\nline2")
	}
	// ctrl+j also inserts a newline.
	m.press(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line3")})
	if got := m.composer.Value(); got != "line1\nline2\nline3" {
		t.Fatalf("ctrl+j newline: %q want %q", got, "line1\nline2\nline3")
	}
	// plain Enter submits (composer clears).
	if _, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("plain Enter returned no submit cmd")
	}
	if got := m.composer.Value(); got != "" {
		t.Fatalf("after Enter submit, composer = %q want empty", got)
	}
}
