package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSubmitComposer_EchoesUserMessage: submitting a message writes it to the
// transcript (Printer) as a "› <text>" cell — otherwise the operator's own
// message vanishes when the composer clears (the engine never echoes it back).
func TestSubmitComposer_EchoesUserMessage(t *testing.T) {
	var printed []string
	m := NewModel(Config{
		Backend: newFakeBackend(),
		AgentID: "agora",
		Theme:   PlainTheme(),
		Printer: capturingPrinter(&printed),
		Now:     func() time.Time { return time.Unix(0, 0).UTC() },
	})
	m.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m.press(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sonnet")})
	m.press(tea.KeyMsg{Type: tea.KeyEnter}) // submit

	joined := strings.Join(printed, "\n")
	if !strings.Contains(joined, "hello sonnet") {
		t.Fatalf("submitted message not echoed to transcript; printed=%q", printed)
	}
	if !strings.Contains(joined, "›") {
		t.Fatalf("user echo missing the '›' cell prefix; printed=%q", printed)
	}
	// The composer must be cleared after submit (the echo is in scrollback now).
	if got := m.composer.Value(); got != "" {
		t.Fatalf("composer not cleared after submit: %q", got)
	}
}

// TestSubmitComposer_EchoEmptyNoop: an empty submit echoes nothing (Submit
// returns sent=false, so there is nothing to echo or send).
func TestSubmitComposer_EchoEmptyNoop(t *testing.T) {
	var printed []string
	m := NewModel(Config{
		Backend: newFakeBackend(),
		AgentID: "agora",
		Theme:   PlainTheme(),
		Printer: capturingPrinter(&printed),
		Now:     func() time.Time { return time.Unix(0, 0).UTC() },
	})
	m.press(tea.KeyMsg{Type: tea.KeyEnter}) // submit nothing
	if len(printed) != 0 {
		t.Fatalf("empty submit printed something: %q", printed)
	}
}
