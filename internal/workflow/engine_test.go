package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
)

// fakeClock is a fixed injected Clock — ground rule: no wall-clock in tests.
type fakeClock struct{ t time.Time }

func (f fakeClock) Now() time.Time { return f.t }

// echoInvoker is a deterministic AgentInvoker: it returns the prompt itself
// (JSON-string-encoded) as the agent's output, and records every prompt it
// was actually asked to run (a LIVE invocation — replayed calls never reach
// this type at all, which is exactly the property the resume tests check).
type echoInvoker struct {
	mu    sync.Mutex
	calls []string
}

func (e *echoInvoker) InvokeAgent(_ context.Context, prompt string, _ AgentCallOpts) (AgentCallResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, prompt)
	e.mu.Unlock()
	out, err := json.Marshal(prompt)
	if err != nil {
		return AgentCallResult{}, err
	}
	return AgentCallResult{Output: out}, nil
}

func (e *echoInvoker) callLog() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

func (e *echoInvoker) reset() {
	e.mu.Lock()
	e.calls = nil
	e.mu.Unlock()
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return b
}

func mustResult(t *testing.T, out Outcome) map[string]any {
	t.Helper()
	if out.Status != StatusCompleted {
		t.Fatalf("Status = %q, want completed (err=%v, parked=%+v)", out.Status, out.Err, out.Parked)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Result, &m); err != nil {
		t.Fatalf("decode result: %v (raw=%s)", err, out.Result)
	}
	return m
}

// --- DoD 1: journal resume property — edited-tail-only reruns ---------

func TestRun_JournalResume_EditedTailOnlyReruns(t *testing.T) {
	script := readTestdata(t, "sequential.star")
	editedScript := readTestdata(t, "sequential_edited.star")

	journal := NewMemJournalStore()
	invoker := &echoInvoker{}
	clock := fakeClock{t: time.Unix(1000, 0).UTC()}
	args := map[string]any{"seed": "x"}

	out1, err := Run(context.Background(), RunOptions{
		RunID: "run-resume-1", Script: script, Args: args,
		Clock: clock, Invoker: invoker, Journal: journal,
	})
	if err != nil {
		t.Fatalf("Run (fresh): %v", err)
	}
	res1 := mustResult(t, out1)
	if res1["a"] != "step1:x" || res1["b"] != "step2:step1:x" || res1["c"] != "step3:step2:step1:x" {
		t.Fatalf("fresh run result = %+v; want the chained echo values", res1)
	}
	if calls := invoker.callLog(); len(calls) != 3 {
		t.Fatalf("fresh run invoked %d agents; want 3: %v", len(calls), calls)
	}

	// Resume with stage 2's prompt edited: stage 1 is untouched -> must
	// replay from cache (no live call); stage 2 (edited) and stage 3
	// (its prompt embeds stage 2's result, which is now different) must
	// both run live.
	invoker.reset()
	out2, err := Run(context.Background(), RunOptions{
		RunID: "run-resume-1", Script: editedScript, Args: args,
		Clock: clock, Invoker: invoker, Journal: journal,
	})
	if err != nil {
		t.Fatalf("Run (resume): %v", err)
	}
	res2 := mustResult(t, out2)
	if res2["a"] != "step1:x" {
		t.Fatalf("resumed run's stage-1 result = %v; want the ORIGINAL cached value step1:x (proves it replayed, not re-ran)", res2["a"])
	}
	if res2["b"] != "step2v2:step1:x" || res2["c"] != "step3:step2v2:step1:x" {
		t.Fatalf("resumed run result = %+v; want the edited chain", res2)
	}

	calls := invoker.callLog()
	if len(calls) != 2 {
		t.Fatalf("resumed run invoked %d agents live; want exactly 2 (stage 2 edited + stage 3 downstream): %v", len(calls), calls)
	}
	for _, c := range calls {
		if c == "step1:x" {
			t.Fatalf("stage 1 was invoked LIVE on resume (calls=%v); the unchanged prefix must replay from cache, never re-invoke the agent", calls)
		}
	}

	// The persisted journal itself must also reflect the same story: three
	// entries, same three (branch,local_seq) positions, stage 1's Hash
	// unchanged from the first attempt (proof the SAME cache key/result
	// carried forward), stages 2/3 updated.
	entries, err := journal.Read("run-resume-1")
	if err != nil {
		t.Fatalf("Read journal: %v", err)
	}
	agentEntries := 0
	for _, e := range entries {
		if e.Kind == EntryAgent {
			agentEntries++
		}
	}
	if agentEntries != 3 {
		t.Fatalf("persisted journal has %d agent entries; want 3", agentEntries)
	}
}

// --- DoD 2: frozen-clock determinism ------------------------------------

func TestRun_FrozenClockDeterminism(t *testing.T) {
	script := readTestdata(t, "now.star")
	frozen := fakeClock{t: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)}
	wantNow := frozen.t.Format(time.RFC3339Nano)

	run := func(runID string) Outcome {
		t.Helper()
		out, err := Run(context.Background(), RunOptions{
			RunID: runID, Script: script, Args: map[string]any{},
			Clock: frozen, Invoker: &echoInvoker{}, Journal: NewMemJournalStore(),
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return out
	}

	out1 := run("now-1")
	out2 := run("now-2")

	res1 := mustResult(t, out1)
	res2 := mustResult(t, out2)

	if res1["now"] != wantNow {
		t.Fatalf("ctx.now = %v; want the frozen clock's own RFC3339Nano encoding %q", res1["now"], wantNow)
	}
	if !reflect.DeepEqual(res1, res2) {
		t.Fatalf("two independent runs of the same script+args+frozen-clock produced different results: %+v vs %+v", res1, res2)
	}
	if string(out1.Result) != string(out2.Result) {
		t.Fatalf("Result bytes differ across independent runs: %s vs %s", out1.Result, out2.Result)
	}
}

// --- shared question-test scaffolding -----------------------------------

func newQuestionRouter(t *testing.T, threadID string) *QuestionRouter {
	t.Helper()
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: threadID, CreatedAt: time.Unix(0, 0).UTC(), Profile: "dev"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	return &QuestionRouter{Log: planning.NewQuestionLog(store), Store: store}
}

// --- DoD 3: answer-replay golden -----------------------------------------

func TestRun_AnswerReplay_Golden(t *testing.T) {
	script := readTestdata(t, "question.star")
	journal := NewMemJournalStore()
	router := newQuestionRouter(t, "th-answer-replay")
	clock := fakeClock{t: time.Unix(2000, 0).UTC()}

	out1, err := Run(context.Background(), RunOptions{
		RunID: "run-answer-replay", ThreadID: "th-answer-replay",
		Script: script, Args: map[string]any{},
		Clock: clock, Invoker: &echoInvoker{}, Journal: journal, Questions: router,
		Identity: "test",
	})
	if err != nil {
		t.Fatalf("Run (asks): %v", err)
	}
	if out1.Status != StatusParked {
		t.Fatalf("Status = %q; want parked (a fresh question always parks first)", out1.Status)
	}
	if out1.Parked == nil || out1.Parked.QuestionID == "" {
		t.Fatalf("Parked info missing/empty: %+v", out1.Parked)
	}

	// Answer it, exactly as an operator would via planning.QuestionLog.
	if err := router.Log.Answer("th-answer-replay", out1.Parked.QuestionID,
		contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "leaning blue", Choice: []string{"blue"}}, By: "operator"},
		time.Unix(2001, 0).UTC(), "operator"); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	// Resume: must replay the recorded answer rather than asking again.
	out2, err := Run(context.Background(), RunOptions{
		RunID: "run-answer-replay", ThreadID: "th-answer-replay",
		Script: script, Args: map[string]any{},
		Clock: clock, Invoker: &echoInvoker{}, Journal: journal, Questions: router,
		Identity: "test",
	})
	if err != nil {
		t.Fatalf("Run (resume after answer): %v", err)
	}
	res2 := mustResult(t, out2)
	if res2["text"] != "leaning blue" {
		t.Fatalf("resumed run's answer.text = %v; want the recorded answer replayed verbatim", res2["text"])
	}
	choice, _ := res2["choice"].([]any)
	if len(choice) != 1 || choice[0] != "blue" {
		t.Fatalf("resumed run's answer.choice = %v; want [blue]", res2["choice"])
	}
}

// --- DoD 4: parked-run recovery via replay-and-re-ask ---------------------

func TestRun_ParkedRunRecovery_ReplayAndReAsk(t *testing.T) {
	script := readTestdata(t, "question.star")
	journal := NewMemJournalStore()
	router := newQuestionRouter(t, "th-recovery")
	clock := fakeClock{t: time.Unix(3000, 0).UTC()}

	opts := RunOptions{
		RunID: "run-recovery", ThreadID: "th-recovery",
		Script: script, Args: map[string]any{},
		Clock: clock, Invoker: &echoInvoker{}, Journal: journal, Questions: router,
		Identity: "test",
	}

	out1, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (first park): %v", err)
	}
	if out1.Status != StatusParked {
		t.Fatalf("Status = %q; want parked", out1.Status)
	}
	firstQID := out1.Parked.QuestionID

	// Simulate a daemon restart / detached resume with the question STILL
	// unanswered: recovery must replay the journal to the unanswered call
	// and re-raise the SAME question (spec §2), not error, not fabricate an
	// answer, and not mint a brand-new question id (which would prove it
	// tried to Ask() again rather than recovering by replay).
	out2, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (recovery, still unanswered): %v", err)
	}
	if out2.Status != StatusParked {
		t.Fatalf("Status = %q; want parked again (still unanswered)", out2.Status)
	}
	if out2.Parked.QuestionID != firstQID {
		t.Fatalf("recovery minted a NEW question id (%s != %s); want the identical still-open question re-surfaced, not re-asked", out2.Parked.QuestionID, firstQID)
	}
	if out2.Parked.Args.Text != "which color?" {
		t.Fatalf("recovered question payload = %+v; want the original card content", out2.Parked.Args)
	}

	// Now answer it and confirm recovery-then-answer also completes
	// normally (belt and suspenders: the recovery path didn't corrupt
	// anything that would prevent a subsequent legitimate answer).
	if err := router.Log.Answer("th-recovery", firstQID,
		contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "red", Choice: []string{"red"}}, By: "operator"},
		time.Unix(3001, 0).UTC(), "operator"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	out3, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (after answer): %v", err)
	}
	res3 := mustResult(t, out3)
	if res3["text"] != "red" {
		t.Fatalf("final result = %+v; want the answer to have flowed through", res3)
	}
}
