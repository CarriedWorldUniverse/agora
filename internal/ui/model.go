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

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// RegisterSubmit lets main.go hand the engine callbacks to the UI
// after the engine has been constructed. Sent once via p.Send right
// after engine.New so the Model can route input submissions and
// read inbox depth.
type RegisterSubmit struct {
	OnSubmit func(text string)
	InboxLen func() int
}

// wsTick is the internal heartbeat that drives the WSConnected
// poll. Tickled at a fixed cadence (see wsTickInterval).
type wsTick struct{}

const wsTickInterval = 1500 * time.Millisecond

// Config bundles construction-time settings for the Model. Populated
// by cmd/agora/main.go from the keyfile + flags.
type Config struct {
	AspectID     string
	OperatorName string
	HistoryDepth int
	InputHistory int
	Logger       *slog.Logger

	// WSConnected returns the current WS state. Used by the TUI's
	// periodic refresh to drive the status-line indicator. Nil-safe
	// (returns "offline" if unset).
	WSConnected func() bool
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
	input      textarea.Model
	vp         viewport.Model
	vpReady    bool // becomes true once the first WindowSizeMsg sizes the viewport
	inboxDepth int
	wsConnected bool

	// unreadBelow counts chat lines (incl. tty echoes, streaming
	// commits, system banners) that landed while the viewport was
	// scrolled away from the bottom. Surfaced in the status line as
	// "↓ N below" so the operator knows fresh content exists offscreen.
	// Reset to 0 on any scroll-to-bottom path (auto-tail, Ctrl-E/End,
	// manual scroll that lands at bottom).
	unreadBelow int

	// filterChatter, when true, hides chat lines whose operatorRelevant
	// flag is false (incoming bus chatter between aspects that doesn't
	// touch the operator). Default true — the operator's chat panel is
	// quiet by default; Ctrl-T toggles to show everything. NEX-118.
	filterChatter bool

	// onSubmit is the callback main.go registers via RegisterSubmit
	// so the Model can hand input lines to the engine. Nil until
	// registered; submissions before registration are silently dropped.
	onSubmit func(text string)
	// inboxLen is similarly registered post-construction; nil-safe.
	inboxLen func() int

	// inputHistory is the ring of past submitted lines, oldest first.
	// Capped at cfg.InputHistory.
	inputHistory []string
	// historyIdx points at the current entry being browsed. -1 means
	// not browsing — the input shows the user's live draft.
	historyIdx int
	// draftSnapshot saves the user's live draft when they begin
	// browsing history, so Down past the newest entry restores it.
	draftSnapshot string

	// liveLine is what the chat panel actually renders for streaming
	// model output (between the chat scroll region and the input
	// prompt). Cleared on ModelTurnEnd; the committed reply lands
	// in chat via ChatSent / ChatPanelReply.
	liveLine string

	// streamBuffer is the raw accumulator of ModelChunk text — used
	// to derive liveLine while masking partial code blocks. See
	// renderStreamingLine.
	streamBuffer string

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

	ta := textarea.New()
	ta.Prompt = "› "
	ta.Placeholder = "type to " + cfg.AspectID + "; shift+enter for newline; /exit to quit"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// Enter submits; shift+enter (and alt+enter) inserts a newline.
	// Default mapping has Enter as insert-newline + ctrl+m as alias;
	// we override so Enter is reserved for submission and the user
	// has to ask for a newline explicitly.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter"),
		key.WithHelp("shift+enter", "insert newline"),
	)
	ta.SetHeight(1)
	ta.Focus()

	return Model{cfg: cfg, input: ta, historyIdx: -1, filterChatter: true}
}

// Init runs once at program start.
func (m Model) Init() tea.Cmd {
	if m.cfg.Logger != nil {
		m.cfg.Logger.Info("ui model initialized",
			"aspect", m.cfg.AspectID,
			"operator", m.cfg.OperatorName)
	}
	return tea.Batch(
		textarea.Blink,
		tea.Tick(wsTickInterval, func(time.Time) tea.Msg { return wsTick{} }),
	)
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
		// textarea width = total width minus the prompt ("› ").
		m.input.SetWidth(max(0, msg.Width-3))
		chatHeight := m.chatHeight()
		firstSize := !m.vpReady
		if firstSize {
			m.vp = viewport.New(msg.Width, chatHeight)
			m.vpReady = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = chatHeight
		}
		// Reflow content at the new width. Snap to bottom only on the
		// first sizing (NEX-248 F6) — subsequent resizes preserve the
		// operator's manual scroll position via refreshChatContent's
		// atBottom check.
		m.refreshChatContent(firstSize)
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
			text := strings.TrimRight(m.input.Value(), " \t\n")
			if text == "" {
				return m, nil
			}
			m.input.SetValue("")
			m.input.SetHeight(1)
			// Record into history. Skip duplicates of the previous
			// entry (so holding-tap on Enter doesn't bloat the ring).
			if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != text {
				m.inputHistory = append(m.inputHistory, text)
				if limit := m.cfg.InputHistory; limit > 0 && len(m.inputHistory) > limit {
					m.inputHistory = m.inputHistory[len(m.inputHistory)-limit:]
				}
			}
			m.historyIdx = -1
			m.draftSnapshot = ""
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
			if m.onSubmit != nil {
				m.onSubmit(text)
			}
			_ = now // reserved for future "submitted at" rendering
			return m, nil
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			// Always route paging to the viewport regardless of
			// input-prompt state — pgup/pgdown have no meaning in
			// a single-line textinput anyway.
			var vpCmd tea.Cmd
			m.vp, vpCmd = m.vp.Update(msg)
			if m.vp.AtBottom() {
				m.unreadBelow = 0
			}
			return m, vpCmd
		case "ctrl+e", "end":
			// Jump-to-bottom (NEX-104). End and Ctrl-E both bind
			// since terminal emulators differ on which they emit;
			// Ctrl-E mirrors readline's "end of line" convention
			// and works in every terminal we've tested.
			if m.vpReady {
				m.vp.GotoBottom()
				m.unreadBelow = 0
			}
			return m, nil
		case "ctrl+t":
			// NEX-118: toggle background-chatter filter. Default is
			// "operator-relevant only"; toggle shows everything for
			// when the operator wants to see what the cluster is
			// doing without leaving agora.
			m.filterChatter = !m.filterChatter
			m.refreshChatContent(false)
			return m, nil
		case "ctrl+a", "home":
			// Jump-to-top. Symmetric with Ctrl-E/End; rarely used
			// (the top of scrollback is usually old context the
			// operator doesn't need to revisit) but cheap to bind.
			if m.vpReady {
				m.vp.GotoTop()
			}
			return m, nil
		}
		// Arrow keys: history first (when there's history to browse),
		// then viewport when input is empty, then fall through to
		// textinput for cursor positioning.
		switch msg.String() {
		case "up":
			// Multi-line cursor up wins when there's content above.
			// At the top line, fall through to history (if any)
			// or viewport (if input is empty).
			if m.input.Line() > 0 {
				break
			}
			if len(m.inputHistory) > 0 {
				m.historyBack()
				return m, nil
			}
			if m.input.Value() == "" {
				var vpCmd tea.Cmd
				m.vp, vpCmd = m.vp.Update(msg)
				return m, vpCmd
			}
		case "down":
			if m.input.LineCount() > 1 && m.input.Line() < m.input.LineCount()-1 {
				break
			}
			if m.historyIdx != -1 {
				m.historyForward()
				return m, nil
			}
			if m.input.Value() == "" {
				var vpCmd tea.Cmd
				m.vp, vpCmd = m.vp.Update(msg)
				return m, vpCmd
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.resizeInputForContent()
		return m, cmd

	case InboxUpdated:
		if m.inboxLen != nil {
			m.inboxDepth = m.inboxLen()
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

	case RegisterSubmit:
		m.onSubmit = msg.OnSubmit
		m.inboxLen = msg.InboxLen
		return m, nil

	case wsTick:
		if m.cfg.WSConnected != nil {
			m.wsConnected = m.cfg.WSConnected()
		}
		return m, tea.Tick(wsTickInterval, func(time.Time) tea.Msg { return wsTick{} })

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
		m.streamBuffer += msg.Text
		m.liveLine = renderStreamingLine(m.streamBuffer)
		return m, nil

	case ModelTurnEnd:
		m.streamBuffer = ""
		m.liveLine = ""
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resizeInputForContent()
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

// historyBack moves one step deeper into input history (toward
// older entries). Saves the live draft on first step. No-op when
// already at the oldest entry.
func (m *Model) historyBack() {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.historyIdx == -1 {
		m.draftSnapshot = m.input.Value()
		m.historyIdx = len(m.inputHistory) - 1
	} else if m.historyIdx > 0 {
		m.historyIdx--
	} else {
		return // already at the oldest
	}
	m.input.SetValue(m.inputHistory[m.historyIdx])
	m.input.CursorEnd()
}

// historyForward moves one step toward newer entries. Past the
// newest entry, restores the live draft.
func (m *Model) historyForward() {
	if m.historyIdx == -1 {
		return
	}
	if m.historyIdx+1 >= len(m.inputHistory) {
		m.historyIdx = -1
		m.input.SetValue(m.draftSnapshot)
		m.draftSnapshot = ""
		m.input.CursorEnd()
		return
	}
	m.historyIdx++
	m.input.SetValue(m.inputHistory[m.historyIdx])
	m.input.CursorEnd()
}

// resizeInputForContent grows the textarea height up to a cap as
// the user inserts newlines, and shrinks it as they delete them.
// Keeps the chat viewport area maximal when no multi-line content
// is present.
func (m *Model) resizeInputForContent() {
	const maxInputLines = 6
	lines := m.input.LineCount()
	if lines < 1 {
		lines = 1
	}
	if lines > maxInputLines {
		lines = maxInputLines
	}
	if m.input.Height() != lines {
		m.input.SetHeight(lines)
	}
}

// chatHeight is the height available to the chat viewport given the
// current chrome state: status (1) + 2 dividers (2) + input (N) +
// liveLine (0 or 1).
func (m Model) chatHeight() int {
	inputLines := 1
	if h := m.input.Height(); h > 0 {
		inputLines = h
	}
	chrome := 3 + inputLines // status + 2 dividers + input
	if m.liveLine != "" {
		chrome++
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
	// NEX-118: annotate operatorRelevant at the append site so the
	// renderer doesn't need to know the operator's name. Every chatLine
	// in m.chat carries the flag; the renderer just reads it.
	line.operatorRelevant = markOperatorRelevant(line.class, line.from, line.body, m.cfg.OperatorName)
	m.chat = appendChatLine(m.chat, line, m.cfg.HistoryDepth)
	m.refreshChatContent(false)
}

// refreshChatContent regenerates the viewport content from m.chat at
// the current width. If forceBottom is true, snap to bottom; otherwise
// only auto-scroll if we were already at the bottom (so manual
// scroll-up sticks).
//
// NEX-104: when the operator is scrolled back from the bottom and new
// chat lands, increment unreadBelow so the status line can surface
// "↓ N below". Reset on any path that brings the viewport to the
// bottom — auto-tail or explicit jump.
func (m *Model) refreshChatContent(forceBottom bool) {
	if !m.vpReady {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(renderChatContent(m.chat, m.vp.Width, m.filterChatter))
	if forceBottom || atBottom {
		m.vp.GotoBottom()
		m.unreadBelow = 0
	} else {
		m.unreadBelow++
	}
}

// renderStatus is the top-of-screen one-line status. Spec §9.1.
func (m Model) renderStatus() string {
	left := headerStyle.Render(fmt.Sprintf("agora · %s", m.cfg.AspectID))
	wsState := "offline"
	if m.wsConnected {
		wsState = "online"
	}
	rightParts := []string{fmt.Sprintf("ws:%s · inbox:%d", wsState, m.inboxDepth)}
	if m.vpReady && !m.vp.AtBottom() && m.unreadBelow > 0 {
		// NEX-104: visual cue when scrolled back from the tail.
		// Operator sees that fresh chat has landed offscreen and
		// can hit Ctrl-E/End to jump back.
		rightParts = append(rightParts, fmt.Sprintf("↓ %d below (Ctrl-E)", m.unreadBelow))
	}
	if !m.filterChatter {
		// NEX-118: indicate when filter is OFF so the operator knows
		// they're seeing all cluster chatter, not the curated view.
		rightParts = append(rightParts, "all-chat (Ctrl-T)")
	}
	right := dimStyle.Render(strings.Join(rightParts, " · "))

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
