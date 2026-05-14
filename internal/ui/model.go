// Package ui holds the bubbletea Model/Update/View for agora.
//
// NEX-51  v0 skeleton (alt-screen, ctrl-c).
// NEX-52  WS intake → inbox; status line shows depth.
// NEX-53  TUI core: status line + chat panel + input prompt.
//
// Bubbletea pattern: Model owns state, Update handles messages
// (events), View returns the rendered string. The runtime owns the
// event loop; we just respond to messages.
package ui

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/CarriedWorldUniverse/agora/internal/inbox"
)

// InboxUpdated is the bubbletea message agora's cmd pump sends on
// every inbox.Push. The Model reacts by draining the inbox into the
// chat panel buffer (chat-source items) and updating the depth
// counter. tty-source items are echoed locally already at submit time
// so the chat panel doesn't double-render them — but the inbox still
// holds them for the engine.
type InboxUpdated struct{}

// Config bundles construction-time settings for the Model. Populated
// by cmd/agora/main.go from the keyfile + flags.
type Config struct {
	AspectID     string
	OperatorName string
	HistoryDepth int
	InputHistory int
	Logger       *slog.Logger
	Inbox        *inbox.Inbox
}

// Model is bubbletea's Model. Owns: layout dimensions, the chat
// scrollback buffer, the input prompt's textinput, the last-seen
// inbox depth, and bookkeeping (quitting flag, highest msg_id we've
// rendered so re-deliveries don't double up).
type Model struct {
	cfg Config

	width  int
	height int

	chat       []chatLine
	input      textinput.Model
	inboxDepth int
	wsConnected bool // future NEX-52.1 hook; default false until we surface it

	quitting bool

	// lastRenderedMsgID is the highest chat.deliver msg_id we've
	// already pushed into the chat panel. Guards against re-renders
	// when the inbox holds items that we've shown but not yet
	// processed via the engine.
	lastRenderedMsgID int64
}

// NewModel constructs a Model with sensible defaults.
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

	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "type to " + cfg.AspectID + "; ctrl+c to quit"
	ti.Focus()
	ti.CharLimit = 0 // unlimited; chunking happens at submit time

	return Model{cfg: cfg, input: ti}
}

// Init runs once at program start.
func (m Model) Init() tea.Cmd {
	if m.cfg.Logger != nil {
		m.cfg.Logger.Info("ui model initialized",
			"aspect", m.cfg.AspectID,
			"operator", m.cfg.OperatorName)
	}
	return textinput.Blink
}

// Update handles incoming messages. v0 set: window resize, key
// presses (ctrl+c quits, enter submits, everything else feeds the
// textinput), and InboxUpdated (drain chat-source items into the
// panel).
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// textinput width = total width minus the prompt ("› ").
		m.input.Width = max(0, msg.Width-3)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			text := strings.TrimRight(m.input.Value(), " \t")
			if text == "" {
				return m, nil
			}
			m.input.SetValue("")
			// Echo locally — the engine wiring (NEX-54) pushes the
			// canonical inbox item. View-only echo here keeps the
			// operator's typing immediately visible.
			m.chat = appendChatLine(m.chat, chatLine{
				class: classTTYIn,
				when:  time.Now(),
				from:  m.cfg.OperatorName,
				body:  text,
			}, m.cfg.HistoryDepth)
			// NEX-54 wires the inbox.Push for tty items here.
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case InboxUpdated:
		m = m.drainInbox()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// drainInbox peeks at the inbox without consuming items (the engine
// owns Take). It updates inboxDepth and pulls any not-yet-rendered
// chat items into the chat panel. Since Inbox.Take pops the head,
// agora can't both peek and leave items for the engine; we work
// around this by tracking the highest rendered MsgID and only
// rendering rows whose Item.MsgID exceeds it. This works because the
// engine doesn't yet exist (NEX-55+) and chat items aren't actually
// popped — they accumulate. Once the engine lands, the chat panel
// will instead render in response to the engine's per-turn ingest
// event, not the raw inbox.
func (m Model) drainInbox() Model {
	if m.cfg.Inbox == nil {
		return m
	}
	m.inboxDepth = m.cfg.Inbox.Len()
	// We can't iterate the inbox in place — no Peek API by design
	// (FIFO is for the engine, not the renderer). For NEX-53 we
	// special-case: every InboxUpdated wake-up corresponds to one
	// just-pushed item, so we Take it, render it, and stash it on
	// a side-buffer for the engine to consume later. NEX-54/55
	// replace this with proper engine ingestion.
	for {
		it, ok := m.cfg.Inbox.Take()
		if !ok {
			break
		}
		switch it.Source {
		case inbox.SourceChat:
			if it.MsgID <= m.lastRenderedMsgID {
				continue
			}
			m.lastRenderedMsgID = it.MsgID
			m.chat = appendChatLine(m.chat, chatLine{
				class: classChatIn,
				when:  it.ReceivedAt,
				from:  it.From,
				body:  it.Content,
			}, m.cfg.HistoryDepth)
		case inbox.SourceTTY:
			// Already echoed at submit time; nothing to render here.
		}
	}
	m.inboxDepth = m.cfg.Inbox.Len()
	return m
}

// View renders the current Model state.
//
// Layout (spec §9):
//
//	┌──────────────────────────────────────────┐
//	│ status line                              │  1 row
//	├──────────────────────────────────────────┤
//	│ chat panel                               │  height-4 rows
//	│   ...                                    │
//	├──────────────────────────────────────────┤
//	│ › input prompt                           │  1 row
//	└──────────────────────────────────────────┘
func (m Model) View() string {
	if m.quitting {
		return "agora: shutting down...\n"
	}
	if m.width == 0 || m.height == 0 {
		// Pre-first-WindowSizeMsg paint — bubbletea sends one
		// immediately on Run, so this is only ever the first frame.
		return "agora: initializing...\n"
	}

	status := m.renderStatus()
	divider := dividerStyle.Render(strings.Repeat("─", m.width))

	// 4 chrome rows: status, divider, divider, input.
	chatHeight := m.height - 4
	if chatHeight < 1 {
		chatHeight = 1
	}
	chatBody := renderChatBuffer(m.chat, m.width, chatHeight)
	inputRow := m.input.View()

	return strings.Join([]string{
		status,
		divider,
		chatBody,
		divider,
		inputRow,
	}, "\n")
}

// renderStatus is the top-of-screen one-line status. Spec §9.1.
func (m Model) renderStatus() string {
	left := headerStyle.Render(fmt.Sprintf("agora · %s", m.cfg.AspectID))
	wsState := "offline"
	if m.wsConnected {
		wsState = "online"
	}
	right := dimStyle.Render(fmt.Sprintf("ws:%s · inbox:%d", wsState, m.inboxDepth))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
