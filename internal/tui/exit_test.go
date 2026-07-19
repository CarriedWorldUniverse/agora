package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// yieldsQuit reports whether a returned tea.Cmd, when run, produces a
// tea.QuitMsg — the only reliable way to assert "this key quits" without
// standing up a real tea.Program (tea.Quit is an opaque func value, so cmds
// can't be compared by identity).
func yieldsQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestIsExitCommand(t *testing.T) {
	quit := []string{"/quit", "/exit", "/q", "  /quit  ", "/QUIT", "/Exit"}
	for _, s := range quit {
		if !isExitCommand(s) {
			t.Errorf("isExitCommand(%q) = false; want true", s)
		}
	}
	// Bare words and near-misses stay ordinary messages (sendable to the model).
	pass := []string{"quit", "exit", "q", "/quitter", "please /quit", "", "/"}
	for _, s := range pass {
		if isExitCommand(s) {
			t.Errorf("isExitCommand(%q) = true; want false (should reach the model)", s)
		}
	}
}

// TestHandleKey_CtrlC_Quits: Ctrl+C is the universal quit — from the plain
// composer AND while a value is half-typed. Without it the operator can only
// kill the process (the live-turn bug that motivated this).
func TestHandleKey_CtrlC_Quits(t *testing.T) {
	m := testModel(newFakeBackend())
	if _, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC}); !yieldsQuit(cmd) {
		t.Fatal("Ctrl+C on empty composer did not quit")
	}
	m.composer.InsertText("half typed")
	if _, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC}); !yieldsQuit(cmd) {
		t.Fatal("Ctrl+C with a half-typed composer did not quit")
	}
}

// TestHandleKey_CtrlD_EmptyOnly: Ctrl+D quits ONLY on an empty composer
// (EOF convention); with text present it must not quit (reserved, no
// accidental exit mid-compose).
func TestHandleKey_CtrlD_EmptyOnly(t *testing.T) {
	m := testModel(newFakeBackend())
	if _, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlD}); !yieldsQuit(cmd) {
		t.Fatal("Ctrl+D on empty composer did not quit")
	}
	m.composer.InsertText("x")
	if _, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlD}); yieldsQuit(cmd) {
		t.Fatal("Ctrl+D with a non-empty composer quit; want no-op")
	}
}

// TestSubmitComposer_SlashQuit: typing "/quit" and pressing Enter quits,
// rather than sending "/quit" to the model as a message.
func TestSubmitComposer_SlashQuit(t *testing.T) {
	m := testModel(newFakeBackend())
	m.composer.InsertText("/quit")
	if _, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); !yieldsQuit(cmd) {
		t.Fatal("Enter on \"/quit\" did not quit")
	}
}
