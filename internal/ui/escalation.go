// Operator-escalation approval modal.
//
// When the broker pushes an escalation.request (a native-API aspect
// asking a human to approve/deny a tool call), the Model opens this
// modal. It is the first modal in agora's TUI — there is no shared
// modal infra to build on, so the state machine, key capture, and
// render all live here.
//
// Lifecycle:
//   - EscalationRequestReceived → newEscalationModal, viewport forced to
//     bottom for prominence (handled in model.go Update).
//   - while active, keystrokes are captured BEFORE the textarea/input:
//     a / ← focus approve, d / → focus deny, y approve, n deny, the note
//     textarea takes the rest, Enter confirms, Esc treated as deny
//     (escalation needs an answer — never a silent dismiss).
//   - confirm emits a tea.Cmd that calls the injected sender then clears
//     the modal via an EscalationResolved msg.
//
// Decision constants mirror nexus frames.EscalationApprove / .Deny on
// the wire but are duplicated here so the UI layer doesn't import the
// frames package.
package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	escalationApprove = "approve"
	escalationDeny    = "deny"
)

// escalationFocus tracks which choice the operator has selected. The
// note textarea is always editable; focus only swaps the highlighted
// approve/deny choice and the decision Enter will confirm.
type escalationFocus int

const (
	focusApprove escalationFocus = iota
	focusDeny
)

// escalationModal holds one in-flight escalation request and the
// operator's working answer. Pointer-held on the Model; nil when no
// escalation is pending.
type escalationModal struct {
	req      EscalationRequestReceived
	focus    escalationFocus
	note     textarea.Model
	decision string // set when confirmed; "" while pending
}

// newEscalationModal builds a fresh modal for the given request with a
// blurred-by-default approve focus and an empty note textarea. Defaults
// to approve focus so the common case (operator glances, hits Enter to
// approve) is one keystroke; deny is an explicit choice.
func newEscalationModal(req EscalationRequestReceived) *escalationModal {
	note := textarea.New()
	note.Prompt = "│ "
	note.Placeholder = "optional note (surfaced to the aspect on deny)…"
	note.ShowLineNumbers = false
	note.CharLimit = 0
	note.SetHeight(2)
	note.Focus()
	return &escalationModal{
		req:   req,
		focus: focusApprove,
		note:  note,
	}
}

// decisionFor returns the wire decision string for the current focus.
func (e *escalationModal) decisionFor() string {
	if e.focus == focusDeny {
		return escalationDeny
	}
	return escalationApprove
}

// EscalationResolved is sent after a decision has been dispatched.
// It clears the modal. Err is non-nil when the
// send failed — the Model surfaces it as a system block so the operator
// knows the answer didn't reach the aspect.
type EscalationResolved struct {
	Decision string
	Err      error
}

// handleEscalationKey processes one keystroke while the modal is active.
// Returns the (possibly mutated) Model, a tea.Cmd, and handled=true when
// the key belonged to the modal. When handled is false the caller falls
// through to normal key routing (it never is here — the modal is modal:
// it owns every key while open, so the operator can't accidentally type
// into the chat input behind it).
func (m Model) handleEscalationKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	e := m.escalation

	// Keys that always control the modal regardless of note content:
	// arrows (focus), Enter (confirm), Esc (explicit deny).
	switch msg.String() {
	case "left":
		e.focus = focusApprove
		return m, nil, true
	case "right":
		e.focus = focusDeny
		return m, nil, true
	case "enter":
		return m.confirmEscalation()
	case "esc":
		// Escalation needs an answer; never a silent dismiss. Treat Esc as
		// an explicit deny so a stray keystroke can't strand the aspect's
		// blocked Request waiting on a decision that never comes.
		e.focus = focusDeny
		return m.confirmEscalation()
	}

	// Single-letter shortcuts (a/d focus, y/n quick-confirm) are only
	// active while the note textarea is empty — otherwise those letters
	// must type into the note (else "too risky" loses its y). Once the
	// operator starts a note, use arrows + Enter for the choice.
	if strings.TrimSpace(e.note.Value()) == "" {
		switch msg.String() {
		case "a":
			e.focus = focusApprove
			return m, nil, true
		case "d":
			e.focus = focusDeny
			return m, nil, true
		case "y":
			e.focus = focusApprove
			return m.confirmEscalation()
		case "n":
			e.focus = focusDeny
			return m.confirmEscalation()
		}
	}

	// Everything else feeds the note textarea.
	var cmd tea.Cmd
	e.note, cmd = e.note.Update(msg)
	return m, cmd, true
}

// confirmEscalation captures the current decision + note, emits the
// send command, and returns the Model with the modal marked confirmed.
// The modal is cleared when the EscalationResolved msg comes back so the
// View can show a brief "sending…" state and the send is observable.
func (m Model) confirmEscalation() (Model, tea.Cmd, bool) {
	e := m.escalation
	decision := e.decisionFor()
	note := strings.TrimSpace(e.note.Value())
	requestID := e.req.RequestID
	aspect := e.req.Aspect
	e.decision = decision

	send := m.escalationSend
	cmd := func() tea.Msg {
		var err error
		if send != nil {
			err = send(aspect, decision, note, requestID)
		}
		return EscalationResolved{Decision: decision, Err: err}
	}
	return m, cmd, true
}

// renderEscalationModal produces the prominent approval panel. Rendered
// by View below the chat body when a modal is active. Distinct red /
// warning framing + a ⚠ glyph so it can't be missed.
func (m Model) renderEscalationModal() string {
	e := m.escalation
	width := m.width - 2
	if width < 20 {
		width = 20
	}

	title := escalationTitleStyle.Render("⚠  ESCALATION — aspect requests approval")

	lines := []string{
		title,
		escalationLabelStyle.Render("aspect: ") + e.req.Aspect,
		escalationLabelStyle.Render("tool:   ") + e.req.Tool,
	}
	if e.req.Reason != "" {
		lines = append(lines, escalationLabelStyle.Render("reason: ")+e.req.Reason)
	}
	if len(e.req.Args) > 0 {
		lines = append(lines, escalationLabelStyle.Render("args:   ")+string(e.req.Args))
	}

	approve := " approve (a/y) "
	deny := " deny (d/n) "
	if e.focus == focusApprove {
		approve = escalationApproveSelStyle.Render("▶approve (a/y)")
		deny = escalationChoiceStyle.Render(" deny (d/n) ")
	} else {
		approve = escalationChoiceStyle.Render(" approve (a/y) ")
		deny = escalationDenySelStyle.Render("▶deny (d/n)")
	}
	choices := approve + "   " + deny
	lines = append(lines, "", choices)
	lines = append(lines, e.note.View())
	lines = append(lines, escalationHintStyle.Render("Enter confirm · Esc = deny · type for note"))

	body := strings.Join(lines, "\n")
	return escalationBoxStyle.Width(width).Render(body)
}

var (
	escalationBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#F87171")).
				Padding(0, 1)
	escalationTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F87171")).Bold(true)
	escalationLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("248")).Bold(true)
	escalationChoiceStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	escalationApproveSelStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#0B1320")).
					Background(lipgloss.Color("#34D399")).Bold(true)
	escalationDenySelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#0B1320")).
				Background(lipgloss.Color("#F87171")).Bold(true)
	escalationHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")).Italic(true)
)
