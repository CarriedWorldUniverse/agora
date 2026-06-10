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
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type Config struct {
	AspectID     string
	Agent        string
	OperatorName string
	HistoryDepth int
	InputHistory int
	Logger       *slog.Logger
	Client       *opclient.Client
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
	connStatus  string
	working     bool
	unreadBelow int

	// now is the model's clock; injectable so tests can pin time.
	now func() time.Time

	// Local-echo bookkeeping: every chat.send appends a pending block
	// AND queues a pendingSend; the broker's chat.deliver echo acks the
	// queue FIFO (content equality as a guard). seenIDs dedupes broker
	// message ids across the echo-reconcile and append paths.
	pendingSends []*pendingSend
	sendSeq      int64
	seenIDs      map[int64]struct{}

	// Observe-driven presence: latest snapshot state per TurnID for the
	// configured agent (replace-by-TurnID). presenceTicking guards the
	// 1s tick chain so only one runs at a time.
	turns           map[string]*turnState
	presenceTicking bool

	// escalation holds the in-flight operator-approval modal, or nil
	// when no escalation is pending. The first (and currently only)
	// modal in agora's TUI; while non-nil it captures every keystroke.
	escalation *escalationModal
	// escalationSend dispatches the operator's decision. It is injectable
	// so tests can substitute a fake sender.
	escalationSend func(aspect, decision, note, requestID string) error

	inputHistory  []string
	historyIdx    int
	draftSnapshot string

	quitting bool

	textareaEnabled bool
	lastSubmitted   string // captured on Enter; used by /retry

	writeClipboard func(string) error
	statusNotice   string

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
		cfg.AspectID = cfg.Agent
	}
	if cfg.AspectID == "" {
		cfg.AspectID = "aspect"
	}
	if cfg.Agent == "" {
		cfg.Agent = cfg.AspectID
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
	textareaEnabled := cfg.Client != nil
	if textareaEnabled {
		ta.Placeholder = "message " + cfg.Agent
		ta.Focus()
	} else {
		ta.Blur()
	}

	m := Model{
		cfg: cfg, input: ta, historyIdx: -1,
		connStatus:        "connecting",
		lastInteractionAt: time.Now(),
		sessionStart:      time.Now(),
		textareaEnabled:   textareaEnabled,
		writeClipboard:    clipboard.WriteAll,
		now:               time.Now,
		seenIDs:           make(map[int64]struct{}),
		turns:             make(map[string]*turnState),
	}
	if cfg.Client != nil {
		m.escalationSend = func(aspect, decision, note, requestID string) error {
			return cfg.Client.EscalationDecide(context.Background(), opclient.EscalationDecisionPayload{
				Aspect:    aspect,
				Decision:  decision,
				Operator:  cfg.OperatorName,
				Note:      note,
				RequestID: requestID,
			})
		}
	}
	return m
}

func (m Model) Init() tea.Cmd {
	if m.cfg.Logger != nil {
		m.cfg.Logger.Info("ui model initialized",
			"aspect", m.cfg.AspectID,
			"operator", m.cfg.OperatorName)
	}
	cmds := []tea.Cmd{
		textarea.Blink,
		tea.Tick(idleTickInterval, func(time.Time) tea.Msg { return idleTick{} }),
	}
	if m.cfg.Client != nil {
		cmds = append(cmds, loadHistoryCmd(m.cfg.Client), subscribeCmd(m.cfg.Client, m.cfg.Agent), waitOpEventCmd(m.cfg.Client))
	}
	return tea.Batch(cmds...)
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
	case QuitGraceful:
		m.quitting = true
		return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return ReadyToQuit{} })
	case ReadyToQuit:
		return m, tea.Quit
	case HistoryLoaded:
		if msg.Err != nil {
			m.appendSystem("history load failed: " + msg.Err.Error())
			m.refreshChatContent(true)
			return m, nil
		}
		sort.SliceStable(msg.Messages, func(i, j int) bool { return msg.Messages[i].ID < msg.Messages[j].ID })
		for _, cm := range msg.Messages {
			m.appendChatMessage(cm)
		}
		m.refreshChatContent(true)
		return m, nil
	case OpEventReceived:
		cmd := m.applyOpEvent(msg.Event)
		return m, tea.Batch(cmd, waitOpEventCmd(m.cfg.Client))
	case opEventPoll:
		return m, waitOpEventCmd(m.cfg.Client)
	case SendFailed:
		m.markPendingFailed(msg.Text, msg.Err)
		m.refreshChatContent(false)
		return m, nil
	case sendEchoTimeout:
		if m.expirePendingSend(msg.seq) {
			m.refreshChatContent(false)
		}
		return m, nil
	case presenceTick:
		m.pruneStaleTurns()
		if !m.presenceActive() {
			m.presenceTicking = false
			return m, nil
		}
		return m, presenceTickCmd()
	case ClearStatusNotice:
		m.statusNotice = ""
		return m, nil
	case idleTick:
		if !m.awaitingReentry && time.Since(m.lastInteractionAt) >= idleThreshold {
			m.idleSince = m.lastInteractionAt
			m.awaitingReentry = true
			m.blocksDuringIdle = 0
		}
		return m, tea.Tick(idleTickInterval, func(time.Time) tea.Msg { return idleTick{} })
	case tea.MouseMsg:
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

func loadHistoryCmd(c *opclient.Client) tea.Cmd {
	return func() tea.Msg {
		msgs, _, err := c.ChatList(context.Background(), 0, 200)
		return HistoryLoaded{Messages: msgs, Err: err}
	}
}

func subscribeCmd(c *opclient.Client, agent string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := c.Subscribe(ctx, "subscribe.chat"); err != nil {
			return SendFailed{Text: "subscribe", Err: err}
		}
		// subscribe.observe requires an aspect; the broker ignores the
		// subscription when the payload is empty.
		if err := c.SubscribeWith(ctx, "subscribe.observe", map[string]any{"aspect": agent}); err != nil {
			return SendFailed{Text: "subscribe", Err: err}
		}
		return nil
	}
}

func waitOpEventCmd(c *opclient.Client) tea.Cmd {
	return func() tea.Msg {
		if c == nil {
			return nil
		}
		select {
		case ev, ok := <-c.Events():
			if !ok {
				return OpEventReceived{Event: opclient.ConnState{Connected: false}}
			}
			return OpEventReceived{Event: ev}
		case <-time.After(500 * time.Millisecond):
			return opEventPoll{}
		}
	}
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
