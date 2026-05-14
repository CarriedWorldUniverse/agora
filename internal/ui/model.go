// Package ui holds the bubbletea Model/Update/View for agora.
//
// v0 skeleton (NEX-51): minimal model that renders "agora starting"
// and exits cleanly on Ctrl-C. Subsequent stories layer:
//
//   NEX-52  WS chat.deliver intake → inbox
//   NEX-53  status line + chat panel + input prompt
//   NEX-54  operator-input → inbox
//   NEX-55  output routing (source-tag → channel)
//   NEX-56  notify_operator render path
//   NEX-57  streaming render
//
// Bubbletea pattern: Model owns state, Update handles messages
// (events), View returns the rendered string. The runtime owns the
// event loop; we just respond to messages.
package ui

import (
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/CarriedWorldUniverse/agora/internal/inbox"
)

// InboxUpdated is the bubbletea message agora's cmd pump sends on
// every inbox.Push. The Model reacts by refreshing whatever view
// state depends on the inbox (status counter today, chat panel in
// NEX-53, engine kickoff in NEX-55+).
type InboxUpdated struct{}

// Config bundles construction-time settings for the Model. Populated
// by cmd/agora/main.go from the keyfile + flags.
type Config struct {
	// AspectID is the canonical aspect name (e.g. "shadow"). Used in
	// the status line, in operator-typed inbox items as the From
	// field's target, and in the chat panel's labels.
	AspectID string

	// OperatorName is the human-readable label for the operator side
	// (e.g. "jacinta"). Appears as the From on tty-sourced inbox
	// items + the `you → <aspect>:` prefix in the chat panel.
	OperatorName string

	// HistoryDepth caps the chat scrollback. 0 = default (1000).
	HistoryDepth int

	// InputHistory caps the REPL up-arrow history. 0 = default (100).
	InputHistory int

	// Logger writes to a file (stdout is reserved for the TUI).
	Logger *slog.Logger

	// Inbox is the shared FIFO queue. The Model reads Len() for the
	// status line; subsequent stories (NEX-53/55) consume items from
	// it as part of the engine kick-off path.
	Inbox *inbox.Inbox
}

// Model is bubbletea's Model. State that survives across Update
// calls. The v0 skeleton holds only the basics; subsequent stories
// add inbox state, WS state, scrollback buffer, input buffer, etc.
type Model struct {
	cfg Config

	width  int
	height int

	// quitting becomes true on the first Ctrl-C / SIGINT. The View
	// briefly displays a shutting-down message before tea.Quit
	// returns.
	quitting bool

	// inboxDepth is the last observed inbox.Len(), refreshed on every
	// InboxUpdated message. The status line renders this so the
	// operator can see queued work at a glance.
	inboxDepth int
}

// NewModel constructs an empty Model with sensible defaults applied.
func NewModel(cfg Config) Model {
	if cfg.HistoryDepth <= 0 {
		cfg.HistoryDepth = 1000
	}
	if cfg.InputHistory <= 0 {
		cfg.InputHistory = 100
	}
	if cfg.OperatorName == "" {
		cfg.OperatorName = "operator"
	}
	if cfg.AspectID == "" {
		cfg.AspectID = "aspect"
	}
	return Model{cfg: cfg}
}

// Init runs once at program start. Returns the first command the
// runtime should execute.
func (m Model) Init() tea.Cmd {
	if m.cfg.Logger != nil {
		m.cfg.Logger.Info("ui model initialized",
			"aspect", m.cfg.AspectID,
			"operator", m.cfg.OperatorName)
	}
	return nil
}

// Update handles incoming messages and returns the next model state
// + any command to fire. For the v0 skeleton, we only handle resize
// + ctrl-c. Subsequent stories layer additional message types.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		}

	case InboxUpdated:
		if m.cfg.Inbox != nil {
			m.inboxDepth = m.cfg.Inbox.Len()
		}
		return m, nil
	}
	return m, nil
}

// View renders the current model state into a string. Bubbletea
// writes this to stdout each frame.
//
// v0 skeleton: a placeholder banner + status line. The full
// chat-panel + input-prompt layout comes in NEX-53.
func (m Model) View() string {
	if m.quitting {
		return "agora: shutting down...\n"
	}

	statusStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#1E90FF"))

	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

	header := statusStyle.Render(fmt.Sprintf("agora — %s @ nexus", m.cfg.AspectID))
	hint := dimStyle.Render(fmt.Sprintf("inbox: %d — ctrl+c to quit", m.inboxDepth))

	// Pad to fill the visible area so alt-screen clears properly even
	// when the terminal is large. Skip if width hasn't been set yet
	// (first frame before WindowSizeMsg).
	body := strings.Repeat("\n", maxInt(0, m.height-4))

	return fmt.Sprintf("%s\n%s\n%s", header, hint, body)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
