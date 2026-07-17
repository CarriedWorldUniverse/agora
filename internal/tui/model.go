package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	tea "github.com/charmbracelet/bubbletea"
)

// This file is the bubbletea lifecycle (§8 build order). It follows §0's
// non-negotiable idea: the transcript is never held in a viewport.Model —
// finalized cells are handed to Printer (tea.Println in production) and
// the Model only ever re-renders (1) the active cell's tail and (2) the
// bottom pane (status row, modal-or-composer, queue preview).

// Printer prints one finalized block of text to the terminal's own
// scrollback and forgets it — the seam over tea.Println so tests can
// capture what would have been printed without a real tty.
type Printer func(text string) tea.Cmd

func teaPrinter(text string) tea.Cmd { return tea.Println(text) }

// Config wires a Model to its backend and (optional) test seams.
type Config struct {
	Backend Backend
	AgentID string
	Model   string
	Theme   Theme
	Printer Printer
	// Now is the model's clock; injectable so status-row rendering tests
	// don't depend on wall-clock time (conventions: "no wall-clock in
	// snapshot paths").
	Now func() time.Time
	// KnownAlias validates a %-override's model alias (§4a). Nil = accept
	// any non-empty alias (used where the caller has no registry to check
	// against yet — see build report on bridle-registry wiring deferral).
	KnownAlias KnownAliasChecker
}

// approvalEntry is one queued approval/question/plan request (§3: "Requests
// queue and interleave with streaming; shown in order").
type approvalEntry struct {
	ID       string
	Kind     contracts.ApprovalKind
	Raw      json.RawMessage
	Question *contracts.QuestionAsked
	Plan     *contracts.PlanArtifact
}

// Model is the bubbletea Model for the lean agora TUI.
type Model struct {
	cfg Config

	width, height int

	composer *Composer
	stream   *StreamState // nil when no turn is in flight

	running bool
	turnID  string

	queue []approvalEntry // FIFO of unresolved requests
	// pendingDeny is set when the operator picked a deny/with-feedback
	// option: the modal closes and focus returns to the composer (§3); the
	// next composer Submit sends this deny instead of a user_message.
	pendingDeny    *approvalEntry
	pendingDenyOpt ModalOption
	modalCursor    int

	statusErr string
}

// NewModel constructs a ready Model. Backend may be nil for pure
// View()/Update() unit tests that never touch the network.
func NewModel(cfg Config) *Model {
	if cfg.Printer == nil {
		cfg.Printer = teaPrinter
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Theme.renderer == nil {
		cfg.Theme = DefaultTheme()
	}
	return &Model{cfg: cfg, composer: NewComposer()}
}

func (m *Model) Init() tea.Cmd {
	if m.cfg.Backend == nil {
		return nil
	}
	return waitForEvent(m.cfg.Backend)
}

// backendEventMsg carries one Event off the backend's channel.
type backendEventMsg struct {
	ev contracts.Event
	ok bool
}

func waitForEvent(b Backend) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-b.Events()
		return backendEventMsg{ev: ev, ok: ok}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case backendEventMsg:
		if !msg.ok {
			return m, nil // backend closed; nothing more to pump
		}
		cmds := m.handleEvent(msg.ev)
		cmds = append(cmds, waitForEvent(m.cfg.Backend))
		return m, tea.Batch(cmds...)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) View() string {
	var b strings.Builder
	if m.stream != nil {
		tail := m.stream.Tail()
		if tail != "" {
			b.WriteString(tail)
			b.WriteString("\n")
		}
	}
	if len(m.composer.Queued()) > 0 {
		b.WriteString(m.cfg.Theme.Muted.Render(fmt.Sprintf("(%d message(s) queued)", len(m.composer.Queued()))))
		b.WriteString("\n")
	}
	b.WriteString(m.renderStatusRow())
	b.WriteString("\n")
	if entry := m.activeModal(); entry != nil {
		b.WriteString(m.renderModal(*entry))
		return b.String()
	}
	b.WriteString(m.renderComposer())
	return b.String()
}

func (m *Model) renderStatusRow() string {
	if !m.running {
		if m.statusErr != "" {
			return m.cfg.Theme.Danger.Render("error: " + m.statusErr)
		}
		return m.cfg.Theme.Muted.Render(fmt.Sprintf("%s · %s · Esc to quit", m.cfg.AgentID, m.cfg.Model))
	}
	return m.cfg.Theme.Muted.Render(fmt.Sprintf("⣾ running · %s · Esc to interrupt", m.cfg.Model))
}

func (m *Model) renderComposer() string {
	prompt := "> "
	if m.pendingDeny != nil {
		prompt = "deny, tell the agent what to do differently > "
	}
	return prompt + m.composer.Value()
}

// activeModal returns the front of the queue, or nil if the queue is empty
// or the operator is mid-deny-with-feedback (composer has focus instead).
func (m *Model) activeModal() *approvalEntry {
	if m.pendingDeny != nil || len(m.queue) == 0 {
		return nil
	}
	return &m.queue[0]
}

func (m *Model) renderModal(e approvalEntry) string {
	var b strings.Builder
	switch e.Kind {
	case contracts.KindQuestion:
		b.WriteString(m.cfg.Theme.Header.Render("? " + e.Question.Args.Text))
		b.WriteString("\n")
		for i, opt := range e.Question.Args.Options {
			cursor := "  "
			if i == m.modalCursor {
				cursor = "> "
			}
			b.WriteString(cursor + opt.Label + "\n")
		}
	case contracts.KindPlan:
		b.WriteString(m.cfg.Theme.Header.Render("Plan review"))
		b.WriteString("\n")
		for _, step := range e.Plan.Steps {
			b.WriteString("  - " + step + "\n")
		}
		opts := PlanModalOptions(unresolvedIDs(e.Plan.OpenQuestions))
		for i, opt := range opts {
			cursor := "  "
			if i == m.modalCursor {
				cursor = "> "
			}
			line := cursor + opt.Label
			if opt.Disabled {
				line += "  (" + opt.DisabledWhy + ")"
			}
			b.WriteString(line + "\n")
		}
	default:
		b.WriteString(m.cfg.Theme.Header.Render(fmt.Sprintf("approve %s?", e.Kind)))
		b.WriteString("\n")
		for i, opt := range ApprovalModalOptions(e.Kind) {
			cursor := "  "
			if i == m.modalCursor {
				cursor = "> "
			}
			b.WriteString(cursor + opt.Label + "\n")
		}
	}
	return b.String()
}

func unresolvedIDs(qs []contracts.QuestionAsked) []string {
	ids := make([]string, len(qs))
	for i, q := range qs {
		ids[i] = q.ID
	}
	return ids
}

// handleEvent routes one backend Event into Model state, returning
// tea.Cmds (Printer calls for finalized content). Kept separate from
// Update's tea.Msg switch so it's directly unit-testable.
func (m *Model) handleEvent(ev contracts.Event) []tea.Cmd {
	var cmds []tea.Cmd
	switch ev.Type {
	case contracts.EvThreadStarted:
		cmds = append(cmds, m.cfg.Printer(Cell{Kind: CellSessionHeader, AgentID: m.cfg.AgentID, Model: m.cfg.Model}.Render(m.width, m.cfg.Theme)[0]))
	case contracts.EvTurnStarted:
		m.running = true
		m.turnID = ev.TurnID
		m.stream = NewStreamState()
	case contracts.EvAgentMessageDelta:
		var p struct {
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if m.stream == nil {
			m.stream = NewStreamState()
		}
		m.stream.Append(p.Delta)
		for _, line := range m.stream.Commit() {
			cmds = append(cmds, m.cfg.Printer(line))
		}
	case contracts.EvTurnCompleted, contracts.EvTurnFailed:
		if m.stream != nil {
			for _, line := range m.stream.Finalize() {
				cmds = append(cmds, m.cfg.Printer(line))
			}
			m.stream = nil
		}
		m.running = false
		for _, q := range m.composer.DrainQueued() {
			cmds = append(cmds, m.cfg.Printer("› "+q))
		}
	case contracts.EvApprovalRequested:
		entry := decodeApprovalRequest(ev.Payload)
		m.queue = append(m.queue, entry)
	case contracts.EvQuestionAsked:
		var q contracts.QuestionAsked
		if err := json.Unmarshal(ev.Payload, &q); err == nil {
			m.queue = append(m.queue, approvalEntry{ID: q.ID, Kind: contracts.KindQuestion, Question: &q})
		}
	case contracts.EvApprovalResolved, contracts.EvQuestionAnswered:
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		m.removeFromQueue(p.ID)
	case contracts.EvError:
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		m.statusErr = p.Message
	}
	return cmds
}

func decodeApprovalRequest(raw json.RawMessage) approvalEntry {
	var wire struct {
		ID      string                 `json:"id"`
		Kind    contracts.ApprovalKind `json:"kind"`
		Payload json.RawMessage        `json:"payload"`
	}
	_ = json.Unmarshal(raw, &wire)
	entry := approvalEntry{ID: wire.ID, Kind: wire.Kind, Raw: wire.Payload}
	switch wire.Kind {
	case contracts.KindQuestion:
		var q contracts.QuestionAsked
		if json.Unmarshal(wire.Payload, &q) == nil {
			entry.Question = &q
		}
	case contracts.KindPlan:
		var p contracts.PlanArtifact
		if json.Unmarshal(wire.Payload, &p) == nil {
			entry.Plan = &p
		}
	}
	return entry
}

func (m *Model) removeFromQueue(id string) {
	if id == "" {
		return
	}
	out := m.queue[:0]
	for _, e := range m.queue {
		if e.ID != id {
			out = append(out, e)
		}
	}
	m.queue = out
	if m.pendingDeny != nil && m.pendingDeny.ID == id {
		m.pendingDeny = nil
	}
	m.modalCursor = 0
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if e := m.activeModal(); e != nil {
		return m.handleModalKey(msg, *e)
	}
	switch msg.String() {
	case "enter":
		return m, m.submitComposer()
	case "backspace":
		m.composer.Backspace()
	case "up":
		m.composer.HistoryUp()
	case "down":
		m.composer.HistoryDown()
	default:
		if msg.Type == tea.KeyRunes {
			m.composer.InsertText(string(msg.Runes))
		}
	}
	return m, nil
}

func (m *Model) handleModalKey(msg tea.KeyMsg, e approvalEntry) (tea.Model, tea.Cmd) {
	if e.Kind == contracts.KindQuestion {
		return m.handleQuestionModalKey(msg, e)
	}
	opts := m.optionsFor(e)
	switch msg.String() {
	case "up":
		if m.modalCursor > 0 {
			m.modalCursor--
		}
	case "down":
		if m.modalCursor < len(opts)-1 {
			m.modalCursor++
		}
	case "esc":
		return m, m.sendEscForModal(e)
	case "enter":
		if m.modalCursor < len(opts) {
			return m, m.chooseOption(e, opts[m.modalCursor])
		}
	}
	return m, nil
}

// handleQuestionModalKey handles the question card (§3): v1 wires
// single-option selection end-to-end (cursor + Enter → BuildQuestionAnswer
// → send). Multi-select toggling and a composer-routed free-text answer
// are NOT wired into the key-handling loop yet — BuildQuestionAnswer
// itself supports both (approval_test.go covers them directly) but this
// Model doesn't yet offer the UI affordance to reach them (a toggle-select
// keybinding and a "answer with typed text" mode). Documented deferral,
// not a silent stub: a free-text-only question (no Options) currently has
// no Enter action here and can only be Esc'd.
func (m *Model) handleQuestionModalKey(msg tea.KeyMsg, e approvalEntry) (tea.Model, tea.Cmd) {
	n := len(e.Question.Args.Options)
	switch msg.String() {
	case "up":
		if m.modalCursor > 0 {
			m.modalCursor--
		}
	case "down":
		if m.modalCursor < n-1 {
			m.modalCursor++
		}
	case "esc":
		return m, m.sendEscForModal(e)
	case "enter":
		if n == 0 || m.modalCursor >= n {
			return m, nil
		}
		label := e.Question.Args.Options[m.modalCursor].Label
		in, err := BuildQuestionAnswer(e.ID, e.Question.Args, QuestionCardChoice{Selected: []string{label}})
		if err != nil {
			m.statusErr = err.Error()
			return m, nil
		}
		m.removeFromQueue(e.ID)
		return m, m.send(in)
	}
	return m, nil
}

func (m *Model) optionsFor(e approvalEntry) []ModalOption {
	switch e.Kind {
	case contracts.KindPlan:
		return PlanModalOptions(unresolvedIDs(e.Plan.OpenQuestions))
	default:
		return ApprovalModalOptions(e.Kind)
	}
}

func (m *Model) sendEscForModal(e approvalEntry) tea.Cmd {
	m.removeFromQueue(e.ID)
	var in contracts.Input
	if e.Kind == contracts.KindQuestion {
		in = EscQuestionAnswer(e.ID)
	} else {
		in, _ = ResolveApproval(e.ID, EscDecision(), "")
	}
	return m.send(in)
}

func (m *Model) chooseOption(e approvalEntry, opt ModalOption) tea.Cmd {
	if opt.RequiresMessage {
		m.pendingDeny = &e
		m.pendingDenyOpt = opt
		m.modalCursor = 0
		return nil
	}
	var in contracts.Input
	var err error
	if e.Kind == contracts.KindPlan {
		in, err = ResolvePlan(e.ID, opt, "")
	} else {
		in, err = ResolveApproval(e.ID, opt, "")
	}
	if err != nil {
		m.statusErr = err.Error()
		return nil
	}
	m.removeFromQueue(e.ID)
	return m.send(in)
}

// submitComposer handles Enter on the composer: either a pending
// deny-with-feedback response, or (later) a normal user_message /
// %-override / slash command. v1 wires the deny-with-feedback path (tested
// directly) plus a plain user_message send.
func (m *Model) submitComposer() tea.Cmd {
	if m.pendingDeny != nil {
		text, sent := m.composer.Submit()
		if !sent {
			return nil
		}
		in, err := ResolveApproval(m.pendingDeny.ID, m.pendingDenyOpt, text)
		m.removeFromQueue(m.pendingDeny.ID)
		if err != nil {
			m.statusErr = err.Error()
			return nil
		}
		return m.send(in)
	}
	text, sent := m.composer.Submit()
	if !sent {
		return nil
	}
	model, effort, rest, isOverride, err := ParseOverride(text, m.cfg.KnownAlias)
	if isOverride {
		if err != nil {
			m.statusErr = err.Error()
			return nil
		}
		return m.send(contracts.Input{Type: contracts.InUserMessage, Text: rest, Model: model, Effort: effort})
	}
	return m.send(contracts.Input{Type: contracts.InUserMessage, Text: text})
}

func (m *Model) send(in contracts.Input) tea.Cmd {
	if m.cfg.Backend == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.cfg.Backend.Send(ctx, in); err != nil {
			return backendEventMsg{ev: contracts.Event{Type: contracts.EvError, Payload: mustMarshalTUI(errPayload{Message: err.Error()})}, ok: true}
		}
		return nil
	}
}

type errPayload struct {
	Message string `json:"message"`
}

func mustMarshalTUI(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
