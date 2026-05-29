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

	// blocks is a slice of *chatBlock (not value) so the strings.Builder
	// inside each block is never copied by slice reallocations. A slice
	// of values would copy on append-grow, tripping Builder.copyCheck at
	// the next appendToActiveBlock and killing bubbletea (NEX-bound
	// follow-up to PRs #14 + #16). Append helpers (appendBlock,
	// markInteraction) clone Builder contents into a fresh *chatBlock
	// rather than copying the caller's value verbatim.
	blocks      []*chatBlock
	input       textarea.Model
	vp          viewport.Model
	vpReady     bool
	inboxDepth  int
	wsConnected bool
	unreadBelow int

	onSubmit func(text string)
	inboxLen func() int

	// escalation holds the in-flight operator-approval modal, or nil
	// when no escalation is pending. The first (and currently only)
	// modal in agora's TUI; while non-nil it captures every keystroke.
	escalation *escalationModal
	// escalationSend dispatches the operator's decision to the bus.
	// Injected via RegisterSubmit so tests can substitute a fake. Args
	// mirror the bus call minus operator (the Model fills operator from
	// cfg.OperatorName / AspectID). Returns the send error.
	escalationSend func(aspect, decision, note, requestID string) error

	inputHistory  []string
	historyIdx    int
	draftSnapshot string

	// activeBlockIdx points at the currently-streaming block in m.blocks;
	// -1 when idle. Set on TurnStarted, mutated by TurnChunk, cleared by
	// TurnDone.
	activeBlockIdx int

	quitting bool

	textareaEnabled bool
	lastSubmitted   string // captured on Enter; used by /retry

	wheelObserved    bool
	wheelCheckExpiry time.Time

	showTimestamps bool

	slashHint    string
	sessionStart time.Time

	// Idle / re-entry tracking.
	lastInteractionAt time.Time
	idleSince         time.Time
	awaitingReentry   bool
	blocksDuringIdle  int
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
	ta.Placeholder = "agora starting…"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter"),
		key.WithHelp("shift+enter", "insert newline"),
	)
	ta.SetHeight(1)
	ta.Blur() // disabled until RegisterSubmit

	return Model{
		cfg: cfg, input: ta, historyIdx: -1, activeBlockIdx: -1,
		lastInteractionAt: time.Now(),
		sessionStart:      time.Now(),
		textareaEnabled:   false,
		wheelCheckExpiry:  time.Now().Add(30 * time.Second),
	}
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
		tea.Tick(idleTickInterval, func(time.Time) tea.Msg { return idleTick{} }),
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
		// Modal capture: while an escalation is pending it owns every
		// keystroke (it is modal). Routes BEFORE the chat input so the
		// operator can't type into the chat behind it.
		if m.escalation != nil {
			nm, cmd, _ := m.handleEscalationKey(msg)
			return nm, cmd
		}
		return m.handleKey(msg)
	case EscalationRequestReceived:
		m.escalation = newEscalationModal(msg)
		// Force the viewport to the bottom so the modal (rendered below
		// the chat body) sits in the operator's immediate view.
		if m.vpReady {
			m.vp.GotoBottom()
			m.unreadBelow = 0
		}
		return m, textarea.Blink
	case EscalationResolved:
		// Decision dispatched (success or failure). Record an audit block
		// then clear the modal so the chat input regains focus.
		decided := "approved"
		if msg.Decision == escalationDeny {
			decided = "denied"
		}
		body := "escalation " + decided
		if msg.Err != nil {
			body += " — SEND FAILED: " + msg.Err.Error()
		}
		m.appendBlock(chatBlock{
			class:     blockSystem,
			speaker:   "system",
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString(body)
		m.escalation = nil
		m.refreshChatContent(true)
		return m, nil
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
		m.escalationSend = msg.OnEscalationDecision
		m.textareaEnabled = true
		m.input.Placeholder = "type to " + m.cfg.AspectID + "; shift+enter for newline; /exit to quit"
		m.input.Focus()
		return m, nil
	case wsTick:
		if m.cfg.WSConnected != nil {
			m.wsConnected = m.cfg.WSConnected()
		}
		return m, tea.Tick(wsTickInterval, func(time.Time) tea.Msg { return wsTick{} })
	case idleTick:
		if !m.awaitingReentry && time.Since(m.lastInteractionAt) >= idleThreshold {
			m.idleSince = m.lastInteractionAt
			m.awaitingReentry = true
			m.blocksDuringIdle = 0
		}
		return m, tea.Tick(idleTickInterval, func(time.Time) tea.Msg { return idleTick{} })
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
		// Reconcile the inline streamed block only when the raw reply
		// carried a notify-operator fence. The block was filled from the
		// model's raw stream (fence included); the engine-stripped
		// FinalText is the canonical inline text. Skipping this when
		// !HadNotify leaves the common path byte-for-byte unchanged.
		if msg.HadNotify && m.activeBlockIdx >= 0 && m.activeBlockIdx < len(m.blocks) {
			if strings.TrimSpace(msg.FinalText) == "" {
				// Reply was nothing but notify content; the red block
				// already carries it. Drop the empty inline block.
				m.dropActiveBlock()
			} else {
				m.blocks[m.activeBlockIdx].body.Reset()
				m.blocks[m.activeBlockIdx].body.WriteString(msg.FinalText)
			}
		}
		m.finishActiveBlock()
		m.refreshChatContent(false)
		return m, nil
	case TurnFailed:
		if m.activeBlockIdx >= 0 {
			m.markActiveBlockFailed(msg.Reason)
		} else {
			// EndTurn already cleared the active block (funnel's
			// ObservabilityHook fires before Return.Handle), so the failure
			// reason can't decorate it. Surface as a standalone system block.
			m.appendBlock(chatBlock{
				class:     blockSystem,
				speaker:   "system",
				createdAt: time.Now(),
			})
			m.blocks[len(m.blocks)-1].body.WriteString("turn failed: " + msg.Reason)
		}
		m.appendBlock(chatBlock{
			class:     blockSystem,
			speaker:   "system",
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString("/retry to re-run this turn")
		m.refreshChatContent(false)
		return m, nil
	case SubmissionDropped:
		m.appendBlock(chatBlock{
			class:     blockSystem,
			speaker:   "system",
			createdAt: time.Now(),
		})
		body := "dropped duplicate — same line submitted " + formatAgo(time.Since(msg.FirstSeen)) + " ago"
		if rem := time.Until(msg.FirstSeen.Add(15 * time.Minute)); rem > 0 {
			body += ". Modify the message or wait " + formatAgo(rem) + " more to resend."
		}
		m.blocks[len(m.blocks)-1].body.WriteString(body)
		m.refreshChatContent(false)
		return m, nil
	case tea.MouseMsg:
		if msg.Type == tea.MouseWheelUp || msg.Type == tea.MouseWheelDown {
			m.wheelObserved = true
		}
		var vpCmd tea.Cmd
		m.vp, vpCmd = m.vp.Update(msg)
		if m.vp.AtBottom() {
			m.unreadBelow = 0
		}
		return m, vpCmd
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
	bottomDivider := dividerStyle.Render(strings.Repeat("─", m.width))

	m.vp.Height = m.chatHeight()
	chatBody := m.vp.View()
	inputRow := m.input.View()

	rows := []string{status, "", chatBody, bottomDivider, inputRow}
	if m.slashHint != "" {
		rows = append(rows, systemStyle.Render(m.slashHint))
	}
	// Escalation modal renders prominently below the chat region — a
	// distinct bordered red panel the operator answers before resuming.
	if m.escalation != nil {
		rows = append(rows, m.renderEscalationModal())
	}
	return strings.Join(rows, "\n")
}
