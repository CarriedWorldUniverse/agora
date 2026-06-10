package ui

import (
	"encoding/json"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// capturedDecision records what the modal dispatched to the fake sender.
type capturedDecision struct {
	aspect, decision, note, requestID string
	called                            bool
}

// newModelWithCapture builds a Model with the escalation sender wired to
// a capture so tests can assert exactly what the modal dispatched.
func newModelWithCapture(cap *capturedDecision, sendErr error) Model {
	m := NewModel(Config{AspectID: "shadow", OperatorName: "operator"})
	m.escalationSend = func(aspect, decision, note, requestID string) error {
		cap.aspect = aspect
		cap.decision = decision
		cap.note = note
		cap.requestID = requestID
		cap.called = true
		return sendErr
	}
	return m
}

// runUpdate applies one msg and returns the concrete Model + cmd.
func runUpdate(m Model, msg tea.Msg) (Model, tea.Cmd) {
	tm, cmd := m.Update(msg)
	return tm.(Model), cmd
}

func TestEscalationRequest_OpensModal(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m, _ = runUpdate(m, EscalationRequestReceived{
		RequestID: "01ABC",
		Aspect:    "anvil",
		Tool:      "Bash",
		Args:      json.RawMessage(`{"command":"rm -rf /tmp/x"}`),
		Reason:    "destructive",
	})
	if m.escalation == nil {
		t.Fatalf("modal not created on EscalationRequestReceived")
	}
	if m.escalation.req.RequestID != "01ABC" || m.escalation.req.Aspect != "anvil" {
		t.Fatalf("modal request not populated: %+v", m.escalation.req)
	}
	// Defaults to approve focus (one-keystroke common case).
	if m.escalation.focus != focusApprove {
		t.Fatalf("default focus: want approve got %d", m.escalation.focus)
	}
}

func TestEscalation_DenyKeySetsFocus(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m, _ = runUpdate(m, EscalationRequestReceived{RequestID: "r1", Aspect: "anvil", Tool: "Bash"})
	m, _ = runUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if m.escalation.focus != focusDeny {
		t.Fatalf("after 'd': want deny focus got %d", m.escalation.focus)
	}
	if got := m.escalation.decisionFor(); got != escalationDeny {
		t.Fatalf("decisionFor after 'd': want %q got %q", escalationDeny, got)
	}
}

func TestEscalation_ApproveKeyResetsFocus(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m, _ = runUpdate(m, EscalationRequestReceived{RequestID: "r1", Aspect: "anvil", Tool: "Bash"})
	m, _ = runUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m, _ = runUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.escalation.focus != focusApprove {
		t.Fatalf("after 'a': want approve focus got %d", m.escalation.focus)
	}
}

// Enter on the default (approve) focus dispatches an approve decision
// with the right correlation id, and the resolved msg clears the modal.
func TestEscalation_EnterApproveDispatchesAndClears(t *testing.T) {
	cap := &capturedDecision{}
	m := newModelWithCapture(cap, nil)
	m, _ = runUpdate(m, EscalationRequestReceived{RequestID: "01XYZ", Aspect: "anvil", Tool: "Bash"})

	// Confirm with Enter (focus is approve by default).
	m, cmd := runUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("confirm produced no cmd")
	}
	// Execute the send cmd; it returns EscalationResolved.
	resolved, ok := cmd().(EscalationResolved)
	if !ok {
		t.Fatalf("confirm cmd did not return EscalationResolved")
	}
	if !cap.called {
		t.Fatalf("escalationSend was not called")
	}
	if cap.decision != escalationApprove {
		t.Fatalf("decision: want %q got %q", escalationApprove, cap.decision)
	}
	if cap.aspect != "anvil" {
		t.Fatalf("aspect: want anvil got %q", cap.aspect)
	}
	if cap.requestID != "01XYZ" {
		t.Fatalf("requestID: want 01XYZ got %q", cap.requestID)
	}
	if resolved.Decision != escalationApprove {
		t.Fatalf("resolved decision: want approve got %q", resolved.Decision)
	}

	// Feeding the resolved msg back clears the modal.
	m, _ = runUpdate(m, resolved)
	if m.escalation != nil {
		t.Fatalf("modal not cleared after EscalationResolved")
	}
}

// 'n' is a one-keystroke deny: sets focus AND confirms.
func TestEscalation_NKeyDeniesImmediately(t *testing.T) {
	cap := &capturedDecision{}
	m := newModelWithCapture(cap, nil)
	m, _ = runUpdate(m, EscalationRequestReceived{RequestID: "r9", Aspect: "wren", Tool: "WebFetch"})
	_, cmd := runUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatalf("'n' produced no cmd")
	}
	_ = cmd()
	if !cap.called || cap.decision != escalationDeny {
		t.Fatalf("'n' did not dispatch deny: %+v", cap)
	}
	if cap.requestID != "r9" {
		t.Fatalf("requestID: want r9 got %q", cap.requestID)
	}
}

// Esc must NOT silently dismiss — it is an explicit deny.
func TestEscalation_EscDeniesNotDismiss(t *testing.T) {
	cap := &capturedDecision{}
	m := newModelWithCapture(cap, nil)
	m, _ = runUpdate(m, EscalationRequestReceived{RequestID: "r2", Aspect: "anvil", Tool: "Bash"})
	_, cmd := runUpdate(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatalf("Esc produced no cmd — it must dispatch a deny, not silently dismiss")
	}
	_ = cmd()
	if !cap.called || cap.decision != escalationDeny {
		t.Fatalf("Esc did not dispatch deny: %+v", cap)
	}
}

// The note typed before confirm reaches the sender, trimmed.
func TestEscalation_NoteForwardedOnDeny(t *testing.T) {
	cap := &capturedDecision{}
	m := newModelWithCapture(cap, nil)
	m, _ = runUpdate(m, EscalationRequestReceived{RequestID: "r3", Aspect: "anvil", Tool: "Bash"})
	// Focus deny via 'd', then type a note (runes feed the textarea),
	// then Enter to confirm.
	m, _ = runUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	for _, r := range "too risky" {
		m, _ = runUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := runUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})
	_ = cmd()
	if cap.note != "too risky" {
		t.Fatalf("note: want %q got %q", "too risky", cap.note)
	}
	if cap.decision != escalationDeny {
		t.Fatalf("decision: want deny got %q", cap.decision)
	}
}

// Send failure still resolves (clears modal) but records the error so
// the operator learns the answer didn't reach the aspect.
func TestEscalation_SendFailureSurfacesError(t *testing.T) {
	cap := &capturedDecision{}
	m := newModelWithCapture(cap, errors.New("not connected"))
	m, _ = runUpdate(m, EscalationRequestReceived{RequestID: "r4", Aspect: "anvil", Tool: "Bash"})
	_, cmd := runUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})
	resolved := cmd().(EscalationResolved)
	if resolved.Err == nil {
		t.Fatalf("expected send error to propagate into EscalationResolved")
	}
	// Apply resolved: modal clears, a system block records the failure.
	m, _ = runUpdate(m, resolved)
	if m.escalation != nil {
		t.Fatalf("modal not cleared after failed send")
	}
	found := false
	for _, b := range m.blocks {
		if b.class == blockSystem && contains(b.body.String(), "SEND FAILED") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no system block recording the send failure")
	}
}

// While the modal is active, a plain rune does NOT submit to chat — it
// is captured by the modal (feeds the note), proving modal key capture
// runs before handleKey. We assert no chat 'you' block was created.
func TestEscalation_CapturesKeysBeforeChatInput(t *testing.T) {
	cap := &capturedDecision{}
	m := newModelWithCapture(cap, nil)
	// Enable the textarea path so a stray key COULD reach chat if not captured.
	m.textareaEnabled = true
	m, _ = runUpdate(m, EscalationRequestReceived{RequestID: "r5", Aspect: "anvil", Tool: "Bash"})
	m, _ = runUpdate(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	for _, b := range m.blocks {
		if b.class == blockYou {
			t.Fatalf("keystroke leaked to chat input while modal active")
		}
	}
	// The 'x' should have landed in the note textarea instead.
	if got := m.escalation.note.Value(); got != "x" {
		t.Fatalf("note did not capture keystroke: got %q", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
