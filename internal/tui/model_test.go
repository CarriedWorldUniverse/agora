package tui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeBackend is an in-memory Backend double: Sends land in Sent, Events()
// is fed by the test via feed.
type fakeBackend struct {
	events chan contracts.Event
	Sent   []contracts.Input
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{events: make(chan contracts.Event, 32)}
}

func (f *fakeBackend) Send(ctx context.Context, in contracts.Input) error {
	f.Sent = append(f.Sent, in)
	return nil
}
func (f *fakeBackend) Events() <-chan contracts.Event { return f.events }
func (f *fakeBackend) Close() error                   { close(f.events); return nil }

// press runs handleKey and, if it returned a Cmd, executes it immediately
// (synchronously) — the same effect bubbletea's runtime has when it drains
// the Cmd, without needing a real tea.Program in tests.
func (m *Model) press(msg tea.KeyMsg) {
	_, cmd := m.handleKey(msg)
	if cmd != nil {
		cmd()
	}
}

func testModel(backend Backend) *Model {
	return NewModel(Config{
		Backend: backend,
		AgentID: "anvil-builder",
		Model:   "frontier:high",
		Theme:   PlainTheme(),
		Now:     func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) },
	})
}

// capturingPrinter records what would have been printed instead of issuing
// a real tea.Println, so tests can assert on the finalized/printed
// transcript without a tty.
func capturingPrinter(out *[]string) Printer {
	return func(text string) tea.Cmd {
		*out = append(*out, text)
		return nil
	}
}

func TestModel_StreamingDelta_CommitsNewlineGatedLines(t *testing.T) {
	m := testModel(nil)
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.handleEvent(contracts.Event{Type: contracts.EvTurnStarted, TurnID: "t1"})
	m.handleEvent(contracts.Event{Type: contracts.EvAgentMessageDelta, Payload: deltaPayload(t, "line one\nline two par")})
	if len(printed) != 1 || printed[0] != "line one" {
		t.Fatalf("printed = %v, want [\"line one\"]", printed)
	}
	if got := m.stream.Tail(); got != "line two par" {
		t.Fatalf("tail = %q", got)
	}
	m.handleEvent(contracts.Event{Type: contracts.EvAgentMessageDelta, Payload: deltaPayload(t, "tial\n")})
	if len(printed) != 2 || printed[1] != "line two partial" {
		t.Fatalf("printed = %v", printed)
	}
	m.handleEvent(contracts.Event{Type: contracts.EvTurnCompleted})
	if m.running {
		t.Fatalf("running = true after turn.completed")
	}
	if m.stream != nil {
		t.Fatalf("stream not cleared after finalize")
	}
}

func deltaPayload(t *testing.T, s string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"delta": s})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestModel_ApprovalRequested_OpensModal_SelectApproveOnceSends(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)

	m.handleEvent(contracts.Event{Type: contracts.EvApprovalRequested, Payload: approvalPayload(t, "req-1", contracts.KindExec, map[string]string{"command": "rm -rf tmp"})})
	if m.activeModal() == nil {
		t.Fatalf("expected an active modal after approval.requested")
	}
	if m.activeModal().ID != "req-1" {
		t.Fatalf("activeModal().ID = %q", m.activeModal().ID)
	}

	// cursor starts at 0 = "Approve once".
	m.press(tea.KeyMsg{Type: tea.KeyEnter})
	if len(backend.Sent) != 1 {
		t.Fatalf("Sent = %v, want 1 input", backend.Sent)
	}
	got := backend.Sent[0]
	if got.Type != contracts.InApprovalResponse || got.ID != "req-1" || got.Decision != contracts.DecisionAllow || got.Scope != contracts.ScopeOnce {
		t.Fatalf("Sent[0] = %+v", got)
	}
	if m.activeModal() != nil {
		t.Fatalf("modal should have closed after resolving")
	}
}

func TestModel_ApprovalRequested_DenyWithFeedback_RoutesToComposer(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.handleEvent(contracts.Event{Type: contracts.EvApprovalRequested, Payload: approvalPayload(t, "req-2", contracts.KindPatch, nil)})

	// Move cursor to the deny option (index 2) and select it.
	m.press(tea.KeyMsg{Type: tea.KeyDown})
	m.press(tea.KeyMsg{Type: tea.KeyDown})
	m.press(tea.KeyMsg{Type: tea.KeyEnter})

	if m.activeModal() != nil {
		t.Fatalf("modal should close; focus should return to composer")
	}
	if len(backend.Sent) != 0 {
		t.Fatalf("nothing should send yet: %v", backend.Sent)
	}
	if m.pendingDeny == nil || m.pendingDeny.ID != "req-2" {
		t.Fatalf("pendingDeny = %+v", m.pendingDeny)
	}

	m.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("use a smaller patch")})
	m.press(tea.KeyMsg{Type: tea.KeyEnter})

	if len(backend.Sent) != 1 {
		t.Fatalf("Sent = %v", backend.Sent)
	}
	got := backend.Sent[0]
	if got.Decision != contracts.DecisionDeny || got.Message != "use a smaller patch" || got.ID != "req-2" {
		t.Fatalf("Sent[0] = %+v", got)
	}
	if m.pendingDeny != nil {
		t.Fatalf("pendingDeny should be cleared after send")
	}
}

func TestModel_Esc_IsExplicitDenyNeverSilent(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.handleEvent(contracts.Event{Type: contracts.EvApprovalRequested, Payload: approvalPayload(t, "req-3", contracts.KindEscalation, nil)})
	m.press(tea.KeyMsg{Type: tea.KeyEsc})
	if len(backend.Sent) != 1 {
		t.Fatalf("Esc must send an explicit decision, got %v", backend.Sent)
	}
	if backend.Sent[0].Decision != contracts.DecisionDeny {
		t.Fatalf("Sent[0] = %+v, want deny", backend.Sent[0])
	}
}

func TestModel_QuestionAsked_SelectOptionSendsAnswer(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	q := contracts.QuestionAsked{
		ID:     "q-1",
		Source: contracts.QuestionFromAgent,
		Args: contracts.QuestionArgs{
			Text:    "which branch?",
			Options: []contracts.QuestionOption{{Label: "main"}, {Label: "dev"}},
		},
	}
	raw, _ := json.Marshal(q)
	m.handleEvent(contracts.Event{Type: contracts.EvQuestionAsked, Payload: raw})

	m.press(tea.KeyMsg{Type: tea.KeyDown})
	m.press(tea.KeyMsg{Type: tea.KeyEnter})

	if len(backend.Sent) != 1 {
		t.Fatalf("Sent = %v", backend.Sent)
	}
	got := backend.Sent[0]
	if got.Type != contracts.InQuestionResponse || got.ID != "q-1" || got.Answer == nil || len(got.Answer.Choice) != 1 || got.Answer.Choice[0] != "dev" {
		t.Fatalf("Sent[0] = %+v", got)
	}
}

func TestModel_PlanGate_AllowDisabledWhileQuestionsOpen(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	plan := contracts.PlanArtifact{
		Steps:         []string{"do a", "do b"},
		OpenQuestions: []contracts.QuestionAsked{{ID: "oq-1"}},
	}
	m.handleEvent(contracts.Event{Type: contracts.EvApprovalRequested, Payload: approvalPayload(t, "plan-1", contracts.KindPlan, nil)})
	// Overwrite the queued entry's Plan directly (approvalPayload's generic
	// payload path doesn't carry a typed plan for this helper's simple
	// map — build it explicitly instead).
	m.queue[0].Plan = &plan

	// cursor 0 = allow (disabled) -> Enter should be rejected, no send.
	m.press(tea.KeyMsg{Type: tea.KeyEnter})
	if len(backend.Sent) != 0 {
		t.Fatalf("Sent = %v, want none (allow disabled)", backend.Sent)
	}
	if m.statusErr == "" {
		t.Fatalf("expected a statusErr explaining the disabled option")
	}
}

func TestModel_ApprovalQueue_MultipleRequestsInterleaveInOrder(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.handleEvent(contracts.Event{Type: contracts.EvApprovalRequested, Payload: approvalPayload(t, "req-a", contracts.KindExec, nil)})
	m.handleEvent(contracts.Event{Type: contracts.EvApprovalRequested, Payload: approvalPayload(t, "req-b", contracts.KindExec, nil)})
	if len(m.queue) != 2 || m.queue[0].ID != "req-a" || m.queue[1].ID != "req-b" {
		t.Fatalf("queue = %v, want FIFO order [req-a, req-b]", m.queue)
	}
	m.press(tea.KeyMsg{Type: tea.KeyEnter}) // resolve req-a
	if m.activeModal() == nil || m.activeModal().ID != "req-b" {
		t.Fatalf("expected req-b now active, got %+v", m.activeModal())
	}
}

func approvalPayload(t *testing.T, id string, kind contracts.ApprovalKind, payload any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(struct {
		ID      string                 `json:"id"`
		Kind    contracts.ApprovalKind `json:"kind"`
		Payload any                    `json:"payload"`
	}{ID: id, Kind: kind, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestModel_View_ComposerAndModal_Golden(t *testing.T) {
	m := testModel(nil)
	m.width = 60
	assertGolden(t, "view_composer_idle", []string{m.View()})

	m.handleEvent(contracts.Event{Type: contracts.EvApprovalRequested, Payload: approvalPayload(t, "req-1", contracts.KindExec, nil)})
	assertGolden(t, "view_modal_exec", []string{m.View()})
}
