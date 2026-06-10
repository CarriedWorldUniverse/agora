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
	m.textareaEnabled = true
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
	m.textareaEnabled = true
	m.input.Focus()
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
	m.textareaEnabled = true
	m.input.Focus()
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

func TestClearStatusNotice(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.statusNotice = "copied message"

	updated, _ := m.Update(ClearStatusNotice{})
	m = updated.(Model)

	if m.statusNotice != "" {
		t.Fatalf("statusNotice = %q, want empty", m.statusNotice)
	}
}
