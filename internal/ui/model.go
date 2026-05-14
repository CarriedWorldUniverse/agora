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
// every inbox.Push. The Model uses it to refresh the inbox-depth
// counter on the status line — chat rendering is driven by the
// separate ChatDelivered message (the bus invokes both on a chat
// frame; the engine consumes the inbox).
type InboxUpdated struct{}

// ChatDelivered carries one chat.deliver frame into the UI for
// rendering. The bus emits these from its OnChat callback so the
// chat panel can render without touching the engine-bound inbox.
type ChatDelivered struct {
	From       string
	Content    string
	MsgID      int64
	ReceivedAt time.Time
}

// ChatSent is emitted by the engine after a successful bus.SendChat
// so the chat panel can mirror what we replied with.
type ChatSent struct {
	To   string
	Body string
}

// ChatPanelReply is emitted by the engine for tty-sourced turns —
// the reply stays local (no bus send) and renders in the chat panel
// only. Spec §8.2.
type ChatPanelReply struct {
	Body string
}

// EngineError is emitted on a turn failure or a bus send failure so
// the operator sees the problem in the panel.
type EngineError struct {
	Source string
	Error  string
}

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
			now := time.Now()
			m.chat = appendChatLine(m.chat, chatLine{
				class: classTTYIn,
				when:  now,
				from:  m.cfg.OperatorName,
				body:  text,
			}, m.cfg.HistoryDepth)
			if m.cfg.Inbox != nil {
				m.cfg.Inbox.Push(inbox.Item{
					Source:     inbox.SourceTTY,
					From:       m.cfg.OperatorName,
					Content:    text,
					ReceivedAt: now,
				})
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case InboxUpdated:
		if m.cfg.Inbox != nil {
			m.inboxDepth = m.cfg.Inbox.Len()
		}
		return m, nil

	case ChatDelivered:
		if msg.MsgID <= m.lastRenderedMsgID {
			return m, nil
		}
		m.lastRenderedMsgID = msg.MsgID
		m.chat = appendChatLine(m.chat, chatLine{
			class: classChatIn,
			when:  msg.ReceivedAt,
			from:  msg.From,
			body:  msg.Content,
		}, m.cfg.HistoryDepth)
		return m, nil

	case ChatSent:
		m.chat = appendChatLine(m.chat, chatLine{
			class: classChatOut,
			when:  time.Now(),
			from:  msg.To,
			body:  msg.Body,
		}, m.cfg.HistoryDepth)
		return m, nil

	case ChatPanelReply:
		m.chat = appendChatLine(m.chat, chatLine{
			class: classModel,
			when:  time.Now(),
			from:  m.cfg.AspectID,
			body:  msg.Body,
		}, m.cfg.HistoryDepth)
		return m, nil

	case EngineError:
		m.chat = appendChatLine(m.chat, chatLine{
			class: classSystem,
			when:  time.Now(),
			body:  fmt.Sprintf("engine error (%s): %s", msg.Source, msg.Error),
		}, m.cfg.HistoryDepth)
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
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
