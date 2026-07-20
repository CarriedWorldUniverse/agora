package tui

import (
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
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

// TestHandleKey_SpaceNotEaten: the space bar arrives as tea.KeySpace (not
// tea.KeyRunes) in bubbletea — it must still be inserted, or multi-word input
// collapses ("hello sonnet" -> "hellosonnet").
func TestHandleKey_SpaceNotEaten(t *testing.T) {
	m := testModel(newFakeBackend())
	m.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	m.press(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("there")})
	if got := m.composer.Value(); got != "hi there" {
		t.Fatalf("composer = %q; want %q (space must not be eaten)", got, "hi there")
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

// runCmdCollect executes a tea.Cmd tree (recursing into batches) and reports
// whether a tea.QuitMsg was produced anywhere in it.
func runCmdCollect(cmd tea.Cmd) (sawQuit bool) {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if runCmdCollect(c) {
				sawQuit = true
			}
		}
	case tea.QuitMsg:
		return true
	}
	return sawQuit
}

// TestExit_WhileRunning_InterruptsNotQueues: the NEX-798 contract. /exit
// during a running turn must NOT be swallowed by queue-while-running — it
// interrupts the turn, marks quitting, and defers the actual quit to the
// interrupted turn's terminal event (so the engine persists the exchange and
// the thread JSONL stays resume-clean).
func TestExit_WhileRunning_InterruptsNotQueues(t *testing.T) {
	old := quitGrace
	quitGrace = time.Millisecond
	defer func() { quitGrace = old }()

	backend := newFakeBackend()
	m := testModel(backend)
	m.handleEvent(contracts.Event{Type: contracts.EvTurnStarted, TurnID: "t1"})
	if !m.running {
		t.Fatal("setup: model not running after turn.started")
	}

	m.composer.SetValue("/exit")
	cmd := m.submitComposer()
	if runCmdCollect(cmd) {
		t.Fatal("/exit while running quit IMMEDIATELY — must wait for the interrupted turn's terminal event")
	}
	if !m.quitting {
		t.Fatal("quitting not set")
	}
	var sawInterrupt bool
	for _, in := range backend.Sent {
		if in.Type == contracts.InInterrupt {
			sawInterrupt = true
		}
		if in.Type == contracts.InUserMessage {
			t.Fatalf("/exit leaked to the model as a user message: %+v", in)
		}
	}
	if !sawInterrupt {
		t.Fatalf("no InInterrupt sent; backend saw %v", backend.Sent)
	}
	if qs := m.composer.DrainQueued(); len(qs) != 0 {
		t.Fatalf("/exit was queued (%v) — the exact bug this fixes", qs)
	}

	// The interrupted turn's terminal event now quits.
	cmds := m.handleEvent(contracts.Event{Type: contracts.EvTurnFailed, TurnID: "t1"})
	if !runCmdCollect(tea.Batch(cmds...)) {
		t.Fatal("terminal event after /exit did not quit")
	}
}

// TestExit_QuitGraceBackstop: if the engine never delivers a terminal event,
// the grace timer quits anyway; a stray grace tick on a healthy session is
// inert.
func TestExit_QuitGraceBackstop(t *testing.T) {
	old := quitGrace
	quitGrace = time.Millisecond
	defer func() { quitGrace = old }()

	backend := newFakeBackend()
	m := testModel(backend)
	m.handleEvent(contracts.Event{Type: contracts.EvTurnStarted, TurnID: "t1"})
	m.composer.SetValue("/quit")
	_ = m.submitComposer()
	if !m.quitting {
		t.Fatal("quitting not set")
	}
	if _, cmd := m.Update(quitGraceMsg{}); !runCmdCollect(cmd) {
		t.Fatal("quitGraceMsg while quitting did not quit")
	}
	m2 := testModel(newFakeBackend())
	if _, cmd := m2.Update(quitGraceMsg{}); runCmdCollect(cmd) {
		t.Fatal("stray quitGraceMsg quit an innocent session")
	}
}

// TestEsc_InterruptsRunningTurn: the status row promises "Esc to interrupt" —
// honor it, without ending the session.
func TestEsc_InterruptsRunningTurn(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.handleEvent(contracts.Event{Type: contracts.EvTurnStarted, TurnID: "t1"})

	m.press(tea.KeyMsg{Type: tea.KeyEsc})
	var sawInterrupt bool
	for _, in := range backend.Sent {
		if in.Type == contracts.InInterrupt {
			sawInterrupt = true
		}
	}
	if !sawInterrupt {
		t.Fatalf("Esc while running sent no InInterrupt; backend saw %v", backend.Sent)
	}
	if m.quitting {
		t.Fatal("Esc-interrupt must NOT quit the session")
	}
	cmds := m.handleEvent(contracts.Event{Type: contracts.EvTurnFailed, TurnID: "t1"})
	if runCmdCollect(tea.Batch(cmds...)) {
		t.Fatal("terminal event after a plain Esc-interrupt quit the session")
	}
}
