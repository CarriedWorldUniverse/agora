// Package ui holds the bubbletea Model/Update/View for agora.
//
// model.go is intentionally small: it holds the Model struct and the
// bubbletea lifecycle (NewModel, Init, Update dispatcher, View).
// Per-message handlers live in:
//   - input.go    keystroke handling, history
//   - blocks.go   block lifecycle, status render
//   - messages.go tea.Msg type declarations
//   - chat.go     block rendering, styles
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
)

type Config struct {
	AspectID     string
	OperatorName string
	HistoryDepth int
	InputHistory int
	Logger       *slog.Logger
	WSConnected  func() bool
}

type Model struct {
	cfg Config

	width  int
	height int

	blocks      []chatBlock
	input       textarea.Model
	vp          viewport.Model
	vpReady     bool
	inboxDepth  int
	wsConnected bool
	unreadBelow int

	onSubmit func(text string)
	inboxLen func() int

	inputHistory  []string
	historyIdx    int
	draftSnapshot string

	// activeBlockIdx points at the currently-streaming block in m.blocks;
	// -1 when idle. Set on TurnStarted, mutated by TurnChunk, cleared by
	// TurnDone.
	activeBlockIdx int

	quitting bool

	lastRenderedMsgID int64
}

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
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter"),
		key.WithHelp("shift+enter", "insert newline"),
	)
	ta.SetHeight(1)
	ta.Focus()

	return Model{cfg: cfg, input: ta, historyIdx: -1, activeBlockIdx: -1}
}

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

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(maxInt(0, msg.Width-3))
		chatHeight := m.chatHeight()
		firstSize := !m.vpReady
		if firstSize {
			m.vp = viewport.New(msg.Width, chatHeight)
			m.vpReady = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = chatHeight
		}
		m.refreshChatContent(firstSize)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case InboxUpdated:
		if m.inboxLen != nil {
			m.inboxDepth = m.inboxLen()
		}
		return m, nil
	case QuitGraceful:
		m.quitting = true
		return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return ReadyToQuit{} })
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
		m.appendBlock(chatBlock{
			class:     blockAspect,
			speaker:   msg.From,
			createdAt: msg.ReceivedAt,
		})
		m.blocks[len(m.blocks)-1].body.WriteString(msg.Content)
		m.refreshChatContent(false)
		return m, nil
	case ChatSent:
		m.appendBlock(chatBlock{
			class:     blockAspect,
			speaker:   m.cfg.AspectID,
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString(msg.Body)
		m.refreshChatContent(false)
		return m, nil
	case ChatPanelReply:
		m.appendBlock(chatBlock{
			class:     blockAspect,
			speaker:   m.cfg.AspectID,
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString(msg.Body)
		m.refreshChatContent(false)
		return m, nil
	case EngineError:
		m.appendBlock(chatBlock{
			class:     blockSystem,
			speaker:   "system",
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString(fmt.Sprintf("engine error (%s): %s", msg.Source, msg.Error))
		m.refreshChatContent(false)
		return m, nil
	case NotifyOperator:
		m.appendBlock(chatBlock{
			class:     blockNotify,
			speaker:   m.cfg.AspectID,
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString(msg.Body)
		m.refreshChatContent(false)
		return m, nil
	case TurnStarted:
		m.appendBlock(chatBlock{
			class:     blockAspectThinking,
			speaker:   m.cfg.AspectID,
			createdAt: time.Now(),
		})
		m.activeBlockIdx = len(m.blocks) - 1
		m.refreshChatContent(false)
		return m, nil
	case TurnChunk:
		m.appendToActiveBlock(msg.Text)
		m.refreshChatContent(false)
		return m, nil
	case TurnDone:
		m.finishActiveBlock()
		m.refreshChatContent(false)
		return m, nil
	case TurnFailed:
		m.markActiveBlockFailed(msg.Reason)
		m.refreshChatContent(false)
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resizeInputForContent()
	return m, cmd
}

func (m Model) View() string {
	if m.quitting {
		return "agora: shutting down...\n"
	}
	if m.width == 0 || m.height == 0 {
		return "agora: initializing...\n"
	}

	status := m.renderStatus()
	divider := dividerStyle.Render(strings.Repeat("─", m.width))

	m.vp.Height = m.chatHeight()
	chatBody := m.vp.View()
	inputRow := m.input.View()

	rows := []string{status, divider, chatBody, divider, inputRow}
	return strings.Join(rows, "\n")
}
