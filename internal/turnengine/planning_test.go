package turnengine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// This file mirrors approval_test.go's rendezvous-testing shape (fake
// provider, drain-to-terminal-event helpers) for the `question`/`plan`
// harness-intrinsic tools (planning.go). Spec: agora-spec-planning-
// questions.md.

// questionCall builds a bridle.ToolInvocation for the `question` tool.
func questionCall(id, text string, blocking bool) bridle.ToolInvocation {
	args, _ := json.Marshal(questionCallArgs{
		Payload:  contracts.QuestionArgs{Text: text},
		Blocking: blocking,
	})
	return bridle.ToolInvocation{ID: id, Name: contracts.ToolQuestion, Args: args}
}

// planCall builds a bridle.ToolInvocation for the `plan` tool.
func planCall(id string, p contracts.PlanArtifact) bridle.ToolInvocation {
	args, _ := json.Marshal(p)
	return bridle.ToolInvocation{ID: id, Name: contracts.ToolPlan, Args: args}
}

// newTestThreadStore builds a MemStore with threadID already Created, for
// tests that want to inspect persisted items afterward via readAllItems.
func newTestThreadStore(t *testing.T, threadID string) *persistence.MemStore {
	t.Helper()
	ms := persistence.NewMemStore()
	if err := ms.Create(contracts.ThreadMeta{ThreadID: threadID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create thread %s: %v", threadID, err)
	}
	return ms
}

// readAllItems replays every item a ThreadStore holds for threadID.
func readAllItems(t *testing.T, store contracts.ThreadStore, threadID string) []contracts.ThreadItem {
	t.Helper()
	it, err := store.Resume(threadID)
	if err != nil {
		t.Fatalf("resume %s: %v", threadID, err)
	}
	defer it.Close()
	var out []contracts.ThreadItem
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		out = append(out, item)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("replay %s: %v", threadID, err)
	}
	return out
}

func hasItemType(items []contracts.ThreadItem, typ contracts.ThreadItemType) bool {
	for _, it := range items {
		if it.Type == typ {
			return true
		}
	}
	return false
}

// drainUntil reads events from ch until pred returns true for one of them
// (returning that event) or a terminal turn event / the deadline is hit
// first — failing the test in either of those cases. terminalIsFailure, if
// true, also fails on turn.completed/turn.failed (used when the caller
// expects to observe pred's event strictly BEFORE the turn ends).
func drainUntil(t *testing.T, ch <-chan contracts.Event, d time.Duration, pred func(contracts.Event) bool) contracts.Event {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("out closed before the expected event arrived")
			}
			if pred(ev) {
				return ev
			}
			if ev.Type == contracts.EvTurnFailed || ev.Type == contracts.EvTurnCompleted {
				t.Fatalf("turn ended (%s) before the expected event arrived", ev.Type)
			}
		case <-deadline:
			t.Fatal("timed out waiting for the expected event")
		}
	}
}

// --- question: non-blocking files and continues ---

// TestManager_Question_NonBlocking_FilesAndContinues: blocking:false must
// never park the thread (no thread.waiting) — the turn completes normally,
// question.asked is emitted, and QuestionLog persists a TIQuestionAsked
// audit item.
func TestManager_Question_NonBlocking_FilesAndContinues(t *testing.T) {
	ms := newTestThreadStore(t, "th_q_nonblocking")
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{questionCall("1", "which registry is canonical?", false)}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_q_nonblocking", provider, WithStore(ms), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "ask, non-blocking"}

	var sawAsked, sawWaiting bool
	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				t.Fatal("out closed before turn.completed")
			}
			switch ev.Type {
			case contracts.EvQuestionAsked:
				sawAsked = true
			case contracts.EvThreadWaiting:
				sawWaiting = true
			case contracts.EvTurnCompleted:
				break loop
			case contracts.EvTurnFailed:
				t.Fatal("turn failed; want turn.completed")
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn.completed")
		}
	}
	if !sawAsked {
		t.Fatal("never observed question.asked")
	}
	if sawWaiting {
		t.Fatal("a non-blocking question must never park (thread.waiting observed)")
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	items := readAllItems(t, ms, "th_q_nonblocking")
	if !hasItemType(items, contracts.TIQuestionAsked) {
		t.Fatal("QuestionLog: no TIQuestionAsked item persisted")
	}
	if hasItemType(items, contracts.TIParked) {
		t.Fatal("a non-blocking question must never persist a TIParked record")
	}
}

// --- question: blocking parks, resumes, answer reaches the model ---

// TestManager_Question_Blocking_ParksAndResumesWithAnswer: blocking:true
// parks the thread (thread.waiting), InQuestionResponse resolves the
// parked waiter, the turn resumes and completes, the model's NEXT step
// sees the answer as the tool_result, and the answer is persisted
// (TIQuestionAnswered + TIResumed).
func TestManager_Question_Blocking_ParksAndResumesWithAnswer(t *testing.T) {
	ms := newTestThreadStore(t, "th_q_block")
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{questionCall("1", "which registry is canonical?", true)}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_q_block", provider, WithStore(ms), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "ask, blocking"}

	var questionID string
	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				t.Fatal("out closed before thread.waiting")
			}
			switch ev.Type {
			case contracts.EvQuestionAsked:
				var q contracts.QuestionAsked
				if err := json.Unmarshal(ev.Payload, &q); err != nil {
					t.Fatalf("decode question.asked: %v", err)
				}
				questionID = q.ID
			case contracts.EvThreadWaiting:
				break loop
			case contracts.EvTurnCompleted, contracts.EvTurnFailed:
				t.Fatalf("turn ended (%s) before parking", ev.Type)
			}
		case <-deadline:
			t.Fatal("timed out waiting for thread.waiting")
		}
	}
	if questionID == "" {
		t.Fatal("never captured the question id from question.asked")
	}

	in <- contracts.Input{
		Type:   contracts.InQuestionResponse,
		ID:     questionID,
		Answer: &contracts.AnswerInput{Text: "cluster-local"},
	}

	answeredEv := drainUntil(t, out, testTimeout, func(ev contracts.Event) bool { return ev.Type == contracts.EvQuestionAnswered })
	var ap struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(answeredEv.Payload, &ap); err != nil || ap.ID != questionID {
		t.Fatalf("question.answered payload = %s; want id=%q", answeredEv.Payload, questionID)
	}

	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed after answering")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	toolMsg := lastToolResultMessage(t, provider.LastRequest())
	if !strings.Contains(toolMsg.Content, "cluster-local") {
		t.Fatalf("tool_result content = %q; want it to carry the answer text", toolMsg.Content)
	}

	items := readAllItems(t, ms, "th_q_block")
	if !hasItemType(items, contracts.TIQuestionAnswered) {
		t.Fatal("no TIQuestionAnswered item persisted")
	}
	if !hasItemType(items, contracts.TIResumed) {
		t.Fatal("no TIResumed item persisted")
	}
}

// --- question: interrupt while parked aborts cleanly, never fabricates ---

// TestManager_Question_Blocking_InterruptAbortsWithoutFabricatingAnAnswer:
// an interrupt arriving while the turn is parked on a blocking question
// aborts the turn (turn.failed{interrupted:true}) exactly like the
// existing approval interrupt path (approval_test.go's
// TestManager_Approval_InterruptDuringPendingApproval) — and the question
// is left unanswered: no TIQuestionAnswered/TIResumed item is ever
// written, only the durable TIParked record (§6: never fabricate, a
// parked thread is durable state).
func TestManager_Question_Blocking_InterruptAbortsWithoutFabricatingAnAnswer(t *testing.T) {
	ms := newTestThreadStore(t, "th_q_interrupt")
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{questionCall("1", "which registry is canonical?", true)}},
		fake.Step{Text: "unreachable"},
	)
	m := NewManager("th_q_interrupt", provider, WithStore(ms), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "ask, blocking, then interrupt"}

	drainUntil(t, out, testTimeout, func(ev contracts.Event) bool { return ev.Type == contracts.EvThreadWaiting })

	in <- contracts.Input{Type: contracts.InInterrupt}

	var sawFailed bool
	deadline := time.After(testTimeout)
loop:
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				t.Fatal("out closed before turn.failed{interrupted:true} was observed")
			}
			if ev.Type == contracts.EvTurnCompleted {
				t.Fatal("turn completed; want turn.failed{interrupted:true}")
			}
			if ev.Type == contracts.EvTurnFailed {
				var p turnFailedPayload
				if err := json.Unmarshal(ev.Payload, &p); err != nil {
					t.Fatalf("decode turn.failed payload: %v", err)
				}
				if !p.Interrupted {
					t.Fatalf("turn.failed payload = %+v; want interrupted:true", p)
				}
				sawFailed = true
				break loop
			}
		case <-deadline:
			t.Fatal("timed out draining out after interrupt — possible deadlock")
		}
	}
	if !sawFailed {
		t.Fatal("never observed turn.failed{interrupted:true}")
	}

	in <- contracts.Input{Type: contracts.InEnd}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned %v; want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Run did not return after end — possible deadlock")
	}

	items := readAllItems(t, ms, "th_q_interrupt")
	if hasItemType(items, contracts.TIQuestionAnswered) {
		t.Fatal("never-fabricate violated: a TIQuestionAnswered item exists for an interrupted, unanswered question")
	}
	if hasItemType(items, contracts.TIResumed) {
		t.Fatal("never-fabricate violated: a TIResumed item exists for an interrupted, unanswered question")
	}
	if !hasItemType(items, contracts.TIParked) {
		t.Fatal("expected the durable TIParked record to remain even though the turn aborted (§6 invariant 2)")
	}
}

// --- plan: two updates, KindPlan through the approval gate ---

// TestManager_Plan_TwoUpdatesThroughApprovalGate: a plain plan update
// (submit:false) records a revision with no gate; a submit:true update
// classifies as KindPlan and flows through the SAME generic approval
// pipeline exec/patch/mcp_tool use — policy-auto in this test (matching
// approval_test.go's allowAllPolicy fixture) so it resolves without a
// client round-trip. Both calls land in PlanLog as two revisions.
func TestManager_Plan_TwoUpdatesThroughApprovalGate(t *testing.T) {
	ms := newTestThreadStore(t, "th_plan")

	plan1 := contracts.PlanArtifact{Phase: contracts.PhasePlan, Steps: []string{"extract the parser"}}
	plan2 := contracts.PlanArtifact{
		Phase:  contracts.PhasePlan,
		Steps:  []string{"extract the parser", "table tests for the grammars"},
		Submit: true,
	}

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{planCall("1", plan1)}},
		fake.Step{ToolCalls: []bridle.ToolInvocation{planCall("2", plan2)}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_plan", provider, WithStore(ms), WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "plan it, then submit"}

	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; want turn.completed (policy-auto: no approval.requested expected)", got)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	items := readAllItems(t, ms, "th_plan")
	var revisions int
	for _, it := range items {
		if it.Type == contracts.TIPlanRevision {
			revisions++
		}
	}
	if revisions != 2 {
		t.Fatalf("plan revisions persisted = %d; want 2", revisions)
	}

	current, found, err := m.planLog.Current("th_plan")
	if err != nil {
		t.Fatalf("PlanLog.Current: %v", err)
	}
	if !found {
		t.Fatal("PlanLog.Current: no revision found")
	}
	if len(current.Steps) != 2 || !current.Submit {
		t.Fatalf("PlanLog.Current = %+v; want the second (submitted) revision", current)
	}
}
