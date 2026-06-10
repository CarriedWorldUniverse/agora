package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestYankLastMessageWritesClipboardAndStatus(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.writeClipboard = func(s string) error {
		if s != "copy me" {
			t.Fatalf("clipboard text = %q, want copy me", s)
		}
		return nil
	}
	m.appendBlock(mkBlock(blockSystem, "system", "skip", time.Now()))
	m.appendBlock(mkBlock(blockAspect, "maren", "copy me", time.Now()))

	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if updated.statusNotice != "copied message" {
		t.Fatalf("statusNotice = %q, want copied message", updated.statusNotice)
	}
	if cmd == nil {
		t.Fatalf("expected clear-status command")
	}
}

func TestYankLastMessageReportsClipboardFailure(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.writeClipboard = func(string) error { return errors.New("no clipboard") }
	m.appendBlock(mkBlock(blockAspect, "maren", "copy me", time.Now()))

	updated, _ := m.yankLastMessage()

	if !strings.Contains(updated.statusNotice, "copy failed: no clipboard") {
		t.Fatalf("statusNotice = %q, want failure", updated.statusNotice)
	}
}

func TestYankLastMessageReportsNoMessage(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.writeClipboard = func(string) error {
		t.Fatalf("clipboard should not be called")
		return nil
	}
	m.appendBlock(mkBlock(blockSystem, "system", "skip", time.Now()))

	updated, _ := m.yankLastMessage()

	if updated.statusNotice != "copy: no message" {
		t.Fatalf("statusNotice = %q, want copy: no message", updated.statusNotice)
	}
}

func TestYankKeyDoesNotStealComposerInput(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.input.SetValue("he")
	called := false
	m.writeClipboard = func(string) error {
		called = true
		return nil
	}
	m.appendBlock(mkBlock(blockAspect, "maren", "copy me", time.Now()))

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if called {
		t.Fatalf("clipboard called while composer had input")
	}
	if got := updated.input.Value(); got != "hey" {
		t.Fatalf("composer value = %q, want hey", got)
	}
}

func TestBracketedPasteYDoesNotTriggerYank(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	called := false
	m.writeClipboard = func(string) error {
		called = true
		return nil
	}
	m.appendBlock(mkBlock(blockAspect, "maren", "copy me", time.Now()))

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true})

	if called {
		t.Fatalf("clipboard called for bracketed paste")
	}
	if got := updated.input.Value(); got != "y" {
		t.Fatalf("composer value = %q, want y", got)
	}
}

// --- Task 10: input ergonomics ---

// Multiline compose is the default path: no flag, no client required.
func TestMultilineComposeDefaultOn(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	if !m.textareaEnabled {
		t.Fatalf("textareaEnabled should default to on without any flag")
	}
	if !m.input.Focused() {
		t.Fatalf("compose textarea should be focused by default")
	}
	// And it is genuinely multiline: alt+enter inserts a newline and the
	// compose area grows with the content.
	m.input.SetValue("line one")
	m.input.CursorEnd()
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if got := updated.input.LineCount(); got < 2 {
		t.Fatalf("alt+enter did not insert a newline: LineCount = %d", got)
	}
	if got := updated.input.Height(); got != 2 {
		t.Fatalf("compose area did not grow with content: Height = %d, want 2", got)
	}
}

// Up-arrow on an empty compose area recalls the previously sent input.
func TestUpArrowRecallsPreviousSentInput(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.input.SetValue("ship NEX-558 when keel's done")
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if got := updated.input.Value(); got != "" {
		t.Fatalf("compose area not cleared after send: %q", got)
	}
	updated, _ = updated.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if got := updated.input.Value(); got != "ship NEX-558 when keel's done" {
		t.Fatalf("up-arrow recall = %q, want the sent input", got)
	}
}

func TestClearStatusNotice(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.statusNotice = "copied message"

	updated, _ := m.Update(ClearStatusNotice{})
	m = updated.(Model)

	if m.statusNotice != "" {
		t.Fatalf("statusNotice = %q, want empty", m.statusNotice)
	}
}
