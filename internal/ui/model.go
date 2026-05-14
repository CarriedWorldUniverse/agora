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
	"github.com/charmbracelet/bubbles/viewport"
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

// NotifyOperator is emitted by the engine's TurnContext.NotifyOperator
// — the only proactive-operator channel available to the per-turn
// model. Renders as a notify-class line in the chat panel; never
// goes to the bus. Spec §8.3.
type NotifyOperator struct {
	Body string
}

// ModelChunk feeds one streamed chunk of model output to the TUI for
// the live-line render (spec §10). The chunk is appended to the live
// line; ModelTurnEnd clears it.
type ModelChunk struct {
	Text string
}

// ModelTurnEnd signals the per-turn engine finished. The Model
// clears the live line — the canonical reply is rendered separately
// via ChatSent / ChatPanelReply / EngineError.
type ModelTurnEnd struct{}

// ReadyToQuit is sent by main.go's shutdown coordinator after the
// deregister + engine drain completes. Model handles it by calling
// tea.Quit — this is the final exit signal.
type ReadyToQuit struct{}

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
	vp         viewport.Model
	vpReady    bool // becomes true once the first WindowSizeMsg sizes the viewport
	inboxDepth int
	wsConnected bool // future NEX-52.1 hook; default false until we surface it

	// liveLine is the streaming model output rendered between the
	// chat panel and the input prompt. Cleared on ModelTurnEnd; the
	// committed reply lands in chat via ChatSent / ChatPanelReply.
	liveLine string

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
		chatHeight := m.chatHeight()
		if !m.vpReady {
			m.vp = viewport.New(msg.Width, chatHeight)
			m.vpReady = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = chatHeight
		}
		// Reflow content at the new width.
		m.refreshChatContent(true)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			// First Ctrl-C: graceful (same path as /exit).
			// Second Ctrl-C while shutting down: hard quit.
			if m.quitting {
				return m, tea.Quit
			}
			m.appendChat(chatLine{
				class: classSystem,
				when:  time.Now(),
				body:  "ctrl-c — deregistering... (press again to force exit)",
			})
			return m, func() tea.Msg { return QuitGraceful{} }
		case "enter":
			text := strings.TrimRight(m.input.Value(), " \t")
			if text == "" {
				return m, nil
			}
			m.input.SetValue("")
			// Slash command? Dispatch via the command processor —
			// skips the chat-and-inbox path entirely on hit.
			if cmd, handled := dispatchCommand(&m, text); handled {
				return m, cmd
			}
			now := time.Now()
			m.appendChat(chatLine{
				class: classTTYIn,
				when:  now,
				from:  m.cfg.OperatorName,
				body:  text,
			})
			if m.cfg.Inbox != nil {
				m.cfg.Inbox.Push(inbox.Item{
					Source:     inbox.SourceTTY,
					From:       m.cfg.OperatorName,
					Content:    text,
					ReceivedAt: now,
				})
			}
			return m, nil
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			// Always route paging to the viewport regardless of
			// input-prompt state — pgup/pgdown have no meaning in
			// a single-line textinput anyway.
			var vpCmd tea.Cmd
			m.vp, vpCmd = m.vp.Update(msg)
			return m, vpCmd
		}
		// Arrow keys when the input is empty → viewport line scroll.
		// When the input has content, arrow keys belong to textinput
		// (cursor movement / future history recall).
		if m.input.Value() == "" {
			switch msg.String() {
			case "up", "down":
				var vpCmd tea.Cmd
				m.vp, vpCmd = m.vp.Update(msg)
				return m, vpCmd
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case InboxUpdated:
		if m.cfg.Inbox != nil {
			m.inboxDepth = m.cfg.Inbox.Len()
		}
		return m, nil

	case QuitGraceful:
		// Mark quitting so a second Ctrl-C path is recognized;
		// trigger tea.Quit after a brief delay so the chat-line
		// announcement is visible. main.go runs deregister + bus
		// teardown after p.Run() returns.
		m.quitting = true
		return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
			return ReadyToQuit{}
		})

	case ReadyToQuit:
		return m, tea.Quit

	case ChatDelivered:
		if msg.MsgID <= m.lastRenderedMsgID {
			return m, nil
		}
		m.lastRenderedMsgID = msg.MsgID
		m.appendChat(chatLine{
			class: classChatIn,
			when:  msg.ReceivedAt,
			from:  msg.From,
			body:  msg.Content,
		})
		return m, nil

	case ChatSent:
		m.appendChat(chatLine{
			class: classChatOut,
			when:  time.Now(),
			from:  msg.To,
			body:  msg.Body,
		})
		return m, nil

	case ChatPanelReply:
		m.appendChat(chatLine{
			class: classModel,
			when:  time.Now(),
			from:  m.cfg.AspectID,
			body:  msg.Body,
		})
		return m, nil

	case EngineError:
		m.appendChat(chatLine{
			class: classSystem,
			when:  time.Now(),
			body:  fmt.Sprintf("engine error (%s): %s", msg.Source, msg.Error),
		})
		return m, nil

	case NotifyOperator:
		m.appendChat(chatLine{
			class: classNotify,
			when:  time.Now(),
			body:  msg.Body,
		})
		return m, nil

	case ModelChunk:
		m.liveLine += msg.Text
		return m, nil

	case ModelTurnEnd:
		m.liveLine = ""
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

	liveRow := ""
	if m.liveLine != "" {
		liveRow = modelStyle.Render(m.cfg.AspectID+":") + " " + m.liveLine
		liveRow = wrapLines(liveRow, m.width)
	}

	// Resize the viewport's height to match current chrome state so
	// the chat region grows/shrinks when the live line appears.
	m.vp.Height = m.chatHeight()
	chatBody := m.vp.View()
	inputRow := m.input.View()

	rows := []string{status, divider, chatBody}
	if liveRow != "" {
		rows = append(rows, liveRow)
	}
	rows = append(rows, divider, inputRow)

	return strings.Join(rows, "\n")
}

// chatHeight is the height available to the chat viewport given the
// current chrome state: status (1) + 2 dividers (2) + input (1) +
// liveLine (0 or 1).
func (m Model) chatHeight() int {
	chrome := 4
	if m.liveLine != "" {
		chrome = 5
	}
	h := m.height - chrome
	if h < 1 {
		h = 1
	}
	return h
}

// appendChat pushes a line onto the ring + refreshes the viewport
// content. Auto-scrolls to bottom when the user was already at the
// bottom; preserves manual scroll position otherwise.
func (m *Model) appendChat(line chatLine) {
	m.chat = appendChatLine(m.chat, line, m.cfg.HistoryDepth)
	m.refreshChatContent(false)
}

// refreshChatContent regenerates the viewport content from m.chat at
// the current width. If forceBottom is true, snap to bottom; otherwise
// only auto-scroll if we were already at the bottom (so manual
// scroll-up sticks).
func (m *Model) refreshChatContent(forceBottom bool) {
	if !m.vpReady {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(renderChatContent(m.chat, m.vp.Width))
	if forceBottom || atBottom {
		m.vp.GotoBottom()
	}
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
