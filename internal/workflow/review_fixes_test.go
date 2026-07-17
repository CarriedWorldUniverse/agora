package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"go.starlark.net/starlark"
)

// This file is the TDD regression suite for the U14 workflows-engine review
// gate (NEX-761): one test per numbered finding from the fix brief. Findings
// are cross-referenced by number in comments throughout engine.go/ctx.go/
// convert.go/canon.go/journal.go.

// --- Finding 1: no starlark step budget / uncancellable infinite loop ----

func TestRun_StepBudget_InfiniteLoopErrorsNotHangs(t *testing.T) {
	script := []byte(`
meta = workflow_meta(name = "infinite-loop", description = "spins forever without a step cap")
def main(ctx, args):
    x = 0
    for i in range(10000000000000):
        x = x + 1
    return {"x": x}
`)
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-infinite-loop", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: &echoInvoker{}, Journal: NewMemJournalStore(),
		MaxSteps: 10_000,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusErrored {
		t.Fatalf("Status = %q; want errored — an uncancellable infinite loop must be terminated by the step budget, not hang forever", out.Status)
	}
}

func TestRun_ContextCancel_AbortsInfiniteLoopPromptly(t *testing.T) {
	script := []byte(`
meta = workflow_meta(name = "infinite-loop-2", description = "spins forever, aborted via context cancellation")
def main(ctx, args):
    x = 0
    for i in range(10000000000000):
        x = x + 1
    return {"x": x}
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before Run even starts: the watcher must observe it essentially immediately
	out, err := Run(ctx, RunOptions{
		RunID: "run-infinite-loop-cancel", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: &echoInvoker{}, Journal: NewMemJournalStore(),
		// Belt-and-suspenders bound so the test can't hang even if
		// cancellation observation were somehow delayed — a tight loop
		// burns through this many steps in well under a second.
		MaxSteps: 50_000_000,
	})
	// A canceled context can be observed as early as the load thread
	// (workflow_meta's own trivial top-level exec — Run returns a plain
	// Go error in that case) or, if it races slightly later, inside
	// mainThread's for-loop (Outcome.Status == StatusErrored). Either is
	// "aborts promptly, fail-closed" — what finding 1 requires; the ONE
	// unacceptable outcome is neither (i.e. a hang, which this test's own
	// completion already disproves) or a silent success.
	if err == nil && out.Status != StatusErrored {
		t.Fatalf("Run returned err=nil, Status=%q, Result=%s; want a prompt fail-closed abort (either a Run() error from the canceled load thread, or Status=errored from the canceled main thread)", out.Status, out.Result)
	}
}

// --- Finding 2: unbounded native-Go recursion -> process crash -----------

func TestToGo_DepthGuardPreventsNativeStackOverflow(t *testing.T) {
	var v starlark.Value = starlark.None
	for i := 0; i < maxConvertDepth+500; i++ {
		v = starlark.NewList([]starlark.Value{v})
	}
	_, err := toGo(v)
	if err == nil {
		t.Fatalf("toGo on a value nested %d deep succeeded; want a depth-guard error, not a silent success (and definitely not a crash)", maxConvertDepth+500)
	}
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("toGo depth-guard error = %v; want errors.Is(err, ErrMaxDepthExceeded)", err)
	}
}

func TestCanonicalJSON_DepthGuardPreventsNativeStackOverflow(t *testing.T) {
	var v any
	for i := 0; i < maxCanonicalizeDepth+500; i++ {
		v = []any{v}
	}
	_, err := canonicalJSON(v)
	if err == nil {
		t.Fatalf("canonicalJSON on a value nested %d deep succeeded; want a depth-guard error, not a silent success (and definitely not a crash)", maxCanonicalizeDepth+500)
	}
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("canonicalJSON depth-guard error = %v; want errors.Is(err, ErrMaxDepthExceeded)", err)
	}
}

// --- Finding 3: nested parallel/pipeline goroutine explosion -------------

func TestRun_LifetimeBranchCap_NestedFanOutErrors(t *testing.T) {
	// 3 outer branches, each spawning a further 3 inner branches: 3 + 9 =
	// 12 total branch-goroutine spawns across the run's lifetime — well
	// past a cap of 5, and no single ctx.parallel call's item count (3)
	// ever approaches PerCallItemCap on its own, proving this is a
	// LIFETIME/nesting cap, not the existing per-call one. The first
	// (nested) ctx.parallel call's own exhaustion is swallowed to None
	// per-thunk (spec: "a failed thunk never aborts its siblings"), so a
	// SECOND, sequential, TOP-LEVEL ctx.parallel call is what actually
	// surfaces the already-exhausted cap as a run error — exactly like the
	// existing PerCallItemCap test surfaces its cap at the top level.
	script := []byte(`
meta = workflow_meta(name = "nested-fanout", description = "nested ctx.parallel exceeding the lifetime branch cap")
def main(ctx, args):
    def inner():
        return ctx.parallel([
            lambda: ctx.agent("i0"),
            lambda: ctx.agent("i1"),
            lambda: ctx.agent("i2"),
        ])
    ctx.parallel([
        lambda: inner(),
        lambda: inner(),
        lambda: inner(),
    ])
    more = ctx.parallel([
        lambda: ctx.agent("more0"),
        lambda: ctx.agent("more1"),
    ])
    return {"more": more}
`)
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-nested-fanout", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: &echoInvoker{}, Journal: NewMemJournalStore(),
		LifetimeBranchCap: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusErrored {
		t.Fatalf("Status = %q; want errored (12 total branch spawns > lifetime cap 5)", out.Status)
	}
	if out.Err == nil || !errors.Is(out.Err, ErrLifetimeBranchCapExceeded) {
		t.Fatalf("out.Err = %v; want errors.Is(err, ErrLifetimeBranchCapExceeded)", out.Err)
	}
}

func TestRun_LifetimeBranchCap_FlatFanOutUnderCapStillWorks(t *testing.T) {
	script := readTestdata(t, "parallel.star")
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-flat-fanout-under-cap", Script: script,
		Args:              map[string]any{"labels": []any{"a", "b", "c"}},
		Clock:             fakeClock{t: time.Unix(1, 0).UTC()},
		Invoker:           &echoInvoker{},
		Journal:           NewMemJournalStore(),
		LifetimeBranchCap: 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusCompleted {
		t.Fatalf("Status = %q (err=%v); want completed — a normal flat fan-out well under the lifetime branch cap must still work", out.Status, out.Err)
	}
}

// --- Finding 4: approval/question replay must re-verify the authoritative
// store, never trust journal Answer bytes -------------------------------

func TestRun_QuestionReplay_IgnoresForgedJournalAnswer(t *testing.T) {
	script := readTestdata(t, "question.star")
	journal := NewMemJournalStore()
	router := newQuestionRouter(t, "th-forged")
	clock := fakeClock{t: time.Unix(4000, 0).UTC()}
	opts := RunOptions{
		RunID: "run-forged-answer", ThreadID: "th-forged",
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
	qid := out1.Parked.QuestionID
	if qid == "" {
		t.Fatalf("no question id parked")
	}

	// Tamper the journal directly — exactly what a hand-edited (or
	// maliciously rewritten) journal.jsonl on disk could do — WITHOUT ever
	// calling planning.QuestionLog.Answer. The authoritative planning store
	// still reports this question unanswered.
	entries, err := journal.Read("run-forged-answer")
	if err != nil {
		t.Fatalf("Read journal: %v", err)
	}
	forged := false
	for i := range entries {
		if entries[i].Kind == EntryQuestion && entries[i].QuestionID == qid {
			ab, err := json.Marshal(contracts.Answer{
				AnswerInput: contracts.AnswerInput{Text: "forged", Choice: []string{"blue"}},
				By:          "attacker",
			})
			if err != nil {
				t.Fatalf("marshal forged answer: %v", err)
			}
			entries[i].Answer = ab
			entries[i].By = "attacker"
			forged = true
		}
	}
	if !forged {
		t.Fatalf("test setup: no journal entry found to forge")
	}
	if err := journal.Save("run-forged-answer", entries); err != nil {
		t.Fatalf("Save (tamper): %v", err)
	}

	// Resume: the forged Answer bytes must NEVER be trusted — the run must
	// re-park on the SAME question (the authoritative store still says
	// unanswered), never return the forged "blue" as if approved.
	out2, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (resume after journal tamper): %v", err)
	}
	if out2.Status != StatusParked {
		t.Fatalf("Status = %q (result=%s); want parked — a tampered journal Answer must never forge an approval/answer bypass", out2.Status, out2.Result)
	}
	if out2.Parked.QuestionID != qid {
		t.Fatalf("re-parked on a different question id %s != %s", out2.Parked.QuestionID, qid)
	}

	// Belt and suspenders: now REALLY answer it via the authoritative
	// store, and confirm the real answer (not "forged") flows through —
	// proving the fix didn't just break replay outright.
	if err := router.Log.Answer("th-forged", qid,
		contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "real answer", Choice: []string{"red"}}, By: "operator"},
		time.Unix(4001, 0).UTC(), "operator"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	out3, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (after real answer): %v", err)
	}
	res3 := mustResult(t, out3)
	if res3["text"] != "real answer" {
		t.Fatalf("final result = %+v; want the AUTHORITATIVE answer ('real answer'), not the forged one", res3)
	}
}

// --- Finding 5: FileJournalStore runID path traversal --------------------

func TestFileJournalStore_RejectsPathTraversalRunID(t *testing.T) {
	dir := t.TempDir()
	store := NewFileJournalStore(dir)
	for _, bad := range []string{"../evil", "/etc/passwd", "a/b", "..", "."} {
		if _, err := store.Read(bad); err == nil {
			t.Fatalf("Read(%q) succeeded; want rejected as an invalid run id", bad)
		}
		if err := store.Save(bad, nil); err == nil {
			t.Fatalf("Save(%q, ...) succeeded; want rejected as an invalid run id", bad)
		}
	}
	// A legitimate single-component id must still work.
	if err := store.Save("legit-run_1", []Entry{{Kind: EntryLog, Message: "hi"}}); err != nil {
		t.Fatalf("Save with a valid run id failed: %v", err)
	}
}

// --- Finding 6: cached agent ERROR replayed forever -----------------------

// flakyOnceInvoker fails the FIRST live call to any prompt in its fail set
// (then never again for that prompt) — the "transient failure, resume
// should retry live" scenario finding 6 fixes.
type flakyOnceInvoker struct {
	mu    sync.Mutex
	fail  map[string]bool
	calls []string
	inner *echoInvoker
}

func newFlakyOnceInvoker(failPrompts ...string) *flakyOnceInvoker {
	fail := make(map[string]bool, len(failPrompts))
	for _, p := range failPrompts {
		fail[p] = true
	}
	return &flakyOnceInvoker{fail: fail, inner: &echoInvoker{}}
}

func (f *flakyOnceInvoker) InvokeAgent(ctx context.Context, prompt string, opts AgentCallOpts) (AgentCallResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, prompt)
	shouldFail := f.fail[prompt]
	if shouldFail {
		f.fail[prompt] = false
	}
	f.mu.Unlock()
	if shouldFail {
		return AgentCallResult{}, fmt.Errorf("transient failure for %q", prompt)
	}
	return f.inner.InvokeAgent(ctx, prompt, opts)
}

func (f *flakyOnceInvoker) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestRun_AgentError_RetriesLiveOnResume_SuccessfulPrefixStaysCached(t *testing.T) {
	script := []byte(`
meta = workflow_meta(name = "retry-demo", description = "stage1 fails once, stage2 depends on it")
def main(ctx, args):
    a = ctx.agent("stage1")
    b = ctx.agent("stage2:" + a)
    return {"a": a, "b": b}
`)
	journal := NewMemJournalStore()
	inv := newFlakyOnceInvoker("stage1")
	clock := fakeClock{t: time.Unix(5000, 0).UTC()}
	opts := RunOptions{RunID: "run-retry", Script: script, Args: map[string]any{}, Clock: clock, Invoker: inv, Journal: journal}

	out1, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (attempt 1): %v", err)
	}
	if out1.Status != StatusErrored {
		t.Fatalf("attempt 1 Status = %q; want errored (stage1's transient failure)", out1.Status)
	}
	if calls := inv.callLog(); len(calls) != 1 || calls[0] != "stage1" {
		t.Fatalf("attempt 1 calls = %v; want exactly [stage1]", calls)
	}

	out2, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (attempt 2, resume): %v", err)
	}
	res2 := mustResult(t, out2)
	if res2["a"] != "stage1" || res2["b"] != "stage2:stage1" {
		t.Fatalf("attempt 2 result = %+v; want stage1 retried live and stage2 chained off it", res2)
	}
	if calls := inv.callLog(); len(calls) != 3 {
		t.Fatalf("attempt 2 cumulative live calls = %v; want 3 total (stage1 failed, stage1 retried live, stage2 live) — a cached ERROR must never suppress the retry", calls)
	}

	// Attempt 3: unmodified script, resume again — the now-SUCCESSFUL
	// stage1/stage2 entries must replay from cache with ZERO new live
	// calls (the error-is-a-cache-miss fix must not also break ordinary
	// replay of a successful prefix — the DoD this unit shipped with).
	out3, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (attempt 3): %v", err)
	}
	res3 := mustResult(t, out3)
	if res3["a"] != "stage1" || res3["b"] != "stage2:stage1" {
		t.Fatalf("attempt 3 result = %+v; want the identical replayed result", res3)
	}
	if calls := inv.callLog(); len(calls) != 3 {
		t.Fatalf("attempt 3 cumulative live calls = %v; want still 3 (both entries replayed from cache, zero NEW live calls)", calls)
	}
}

// --- Finding 7: no recover() -> panic crashes the process; sem leak ------

type panicOnPromptInvoker struct{ panicOn string }

func (p panicOnPromptInvoker) InvokeAgent(_ context.Context, prompt string, _ AgentCallOpts) (AgentCallResult, error) {
	if prompt == p.panicOn {
		panic("boom: " + prompt)
	}
	out, err := json.Marshal(prompt)
	if err != nil {
		return AgentCallResult{}, err
	}
	return AgentCallResult{Output: out}, nil
}

// TestRun_PanicInInvoker_RecoveredAsStageError_ProcessSurvives is itself the
// proof: without finding 7's recover(), a panicking invoker crashes the
// WHOLE test binary (not just fails a t.Error) — this test completing and
// reporting a clean StatusErrored IS the regression check.
func TestRun_PanicInInvoker_RecoveredAsStageError_ProcessSurvives(t *testing.T) {
	script := []byte(`
meta = workflow_meta(name = "panic-demo", description = "the invoker panics on this call")
def main(ctx, args):
    return {"r": ctx.agent("boom")}
`)
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-panic", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: panicOnPromptInvoker{panicOn: "boom"}, Journal: NewMemJournalStore(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusErrored {
		t.Fatalf("Status = %q; want errored — a panicking invoker must become a stage error, not crash the process", out.Status)
	}
}

func TestRun_PanicInParallelBranch_DoesNotLeakConcurrencySemaphore(t *testing.T) {
	// MaxConcurrent=1 forces the second branch's beginAgentCall to wait for
	// (and successfully reuse) the FIRST branch's semaphore slot — which
	// only works if the panicking first branch's endAgentCall is deferred,
	// not skipped by the panic.
	script := []byte(`
meta = workflow_meta(name = "panic-parallel", description = "one branch's agent call panics under MaxConcurrent=1")
def main(ctx, args):
    results = ctx.parallel([
        lambda: ctx.agent("boom"),
        lambda: ctx.agent("fine"),
    ])
    return {"results": results}
`)
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-panic-parallel", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: panicOnPromptInvoker{panicOn: "boom"}, Journal: NewMemJournalStore(),
		MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusCompleted {
		t.Fatalf("Status = %q (err=%v); want completed — a panicking agent call in one parallel branch fails that branch to None (spec: a failed thunk never aborts siblings) without leaking the concurrency semaphore", out.Status, out.Err)
	}
	res := mustResult(t, out)
	results, _ := res["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %+v; want 2 entries", res["results"])
	}
	foundFine := false
	for _, r := range results {
		if r == "fine" {
			foundFine = true
		}
	}
	if !foundFine {
		t.Fatalf("results = %v; want the sibling's successful agent call to still complete — proves the panic didn't permanently starve the concurrency semaphore", results)
	}
}

// --- Finding 8: concurrent first-time sibling questions: loser's
// ErrAlreadyParked silently dropped -----------------------------------

func TestRun_ConcurrentSiblingQuestions_BothSurfacedNeverSilentlyDropped(t *testing.T) {
	script := []byte(`
meta = workflow_meta(name = "concurrent-questions", description = "two sibling branches each ask a distinct first-time question")
def main(ctx, args):
    results = ctx.parallel([
        lambda: ctx.question({"text": "color?", "options": [{"label": "red"}]}),
        lambda: ctx.question({"text": "size?", "options": [{"label": "large"}]}),
    ])
    return {"results": results}
`)
	journal := NewMemJournalStore()
	router := newQuestionRouter(t, "th-concurrent-q")
	clock := fakeClock{t: time.Unix(6000, 0).UTC()}
	opts := RunOptions{
		RunID: "run-concurrent-q", ThreadID: "th-concurrent-q",
		Script: script, Args: map[string]any{},
		Clock: clock, Invoker: &echoInvoker{}, Journal: journal, Questions: router,
		Identity: "test",
	}
	out, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusParked {
		t.Fatalf("Status = %q; want parked (the losing sibling's question must surface as a park, never a silent error/None)", out.Status)
	}

	qCount := 0
	for _, e := range out.Entries {
		if e.Kind == EntryQuestion {
			qCount++
		}
	}
	if qCount != 2 {
		t.Fatalf("journal has %d EntryQuestion markers; want 2 — BOTH sibling questions must leave a journal trace, never exactly 1-of-2 silently dropped", qCount)
	}
}

// --- Finding 9: frozen instant not persisted for resume ------------------

func TestRun_FrozenNow_PersistsAcrossResumeDespiteDifferentClock(t *testing.T) {
	script := readTestdata(t, "now.star")
	journal := NewMemJournalStore()

	clock1 := fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	out1, err := Run(context.Background(), RunOptions{
		RunID: "run-frozen-now", Script: script, Args: map[string]any{},
		Clock: clock1, Invoker: &echoInvoker{}, Journal: journal,
	})
	if err != nil {
		t.Fatalf("Run (fresh): %v", err)
	}
	res1 := mustResult(t, out1)

	// Resume with a DIFFERENT clock instant entirely — the run's frozen
	// ctx.now must still be the ORIGINAL one, not the new clock's reading.
	clock2 := fakeClock{t: time.Date(2099, 12, 31, 23, 59, 0, 0, time.UTC)}
	out2, err := Run(context.Background(), RunOptions{
		RunID: "run-frozen-now", Script: script, Args: map[string]any{},
		Clock: clock2, Invoker: &echoInvoker{}, Journal: journal,
	})
	if err != nil {
		t.Fatalf("Run (resume, different clock): %v", err)
	}
	res2 := mustResult(t, out2)

	if res1["now"] != res2["now"] {
		t.Fatalf("ctx.now changed across resume: %v -> %v; want the SAME frozen instant reused from the journal, not a fresh clock reading", res1["now"], res2["now"])
	}
	if res2["now"] == clock2.t.Format(time.RFC3339Nano) {
		t.Fatalf("resume picked up the NEW clock's instant (%v); want the original persisted one", res2["now"])
	}
}

// --- Finding 10: bubbled agent-question permanently caches None ----------

// bubblingInvoker always bubbles the same question, never a normal
// completion — the finding-10 scenario: a human answer must flow back to
// the script instead of being discarded forever.
type bubblingInvoker struct{}

func (bubblingInvoker) InvokeAgent(context.Context, string, AgentCallOpts) (AgentCallResult, error) {
	return AgentCallResult{Question: &contracts.QuestionAsked{
		Args: contracts.QuestionArgs{
			Text:    "which color?",
			Options: []contracts.QuestionOption{{Label: "red"}, {Label: "blue"}},
		},
	}}, nil
}

func TestRun_BubbledAgentQuestion_AnswerFlowsBackToScript(t *testing.T) {
	script := []byte(`
meta = workflow_meta(name = "bubble-demo", description = "ctx.agent()'s underlying agent raises a question")
def main(ctx, args):
    ans = ctx.agent("ask the user")
    return {"ans": ans}
`)
	journal := NewMemJournalStore()
	router := newQuestionRouter(t, "th-bubble")
	clock := fakeClock{t: time.Unix(7000, 0).UTC()}
	opts := RunOptions{
		RunID: "run-bubble", ThreadID: "th-bubble",
		Script: script, Args: map[string]any{},
		Clock: clock, Invoker: bubblingInvoker{}, Journal: journal, Questions: router,
		Identity: "test",
	}

	out1, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (asks): %v", err)
	}
	if out1.Status != StatusParked {
		t.Fatalf("attempt 1 Status = %q; want parked", out1.Status)
	}
	qid := out1.Parked.QuestionID
	if qid == "" {
		t.Fatalf("no question id parked")
	}

	if err := router.Log.Answer("th-bubble", qid,
		contracts.Answer{AnswerInput: contracts.AnswerInput{Text: "leaning blue", Choice: []string{"blue"}}, By: "operator"},
		time.Unix(7001, 0).UTC(), "operator"); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	out2, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (resume after answer): %v", err)
	}
	res2 := mustResult(t, out2)
	ans2, ok := res2["ans"].(map[string]any)
	if !ok || ans2["text"] != "leaning blue" {
		t.Fatalf("resumed run's ctx.agent() result = %+v; want the human's answer to actually reach the script (previously silently discarded, permanently caching None)", res2["ans"])
	}

	// A LATER resume (unmodified script) must still return the SAME
	// answer, not None — proving it's durably cached, not a one-shot fluke.
	out3, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run (later resume): %v", err)
	}
	res3 := mustResult(t, out3)
	ans3, ok := res3["ans"].(map[string]any)
	if !ok || ans3["text"] != "leaning blue" {
		t.Fatalf("attempt 3 (later resume) ctx.agent() result = %+v; want the SAME answer replayed, not None", res3["ans"])
	}
}

// --- Finding 11: concurrent FileJournalStore.Save races -------------------

func TestFileJournalStore_ConcurrentRecordCalls_NoLostEntries(t *testing.T) {
	dir := t.TempDir()
	store := NewFileJournalStore(dir)

	// 40 sibling ctx.parallel branches each doing exactly one ctx.log —
	// every branch's rs.record() concurrently snapshots-and-Saves the
	// SAME runID's journal via FileJournalStore. Without finding 11's fix
	// this could lose entries (a stale snapshot landing after a newer
	// one) or surface a spurious rename/ENOENT error.
	script := []byte(`
meta = workflow_meta(name = "log-fanout", description = "many concurrent ctx.log calls racing FileJournalStore.Save")
def main(ctx, args):
    ctx.parallel([
        lambda i = i: ctx.log("line-" + str(i))
        for i in range(40)
    ])
    return {"ok": True}
`)
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-file-journal-concurrency", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: &echoInvoker{}, Journal: store,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusCompleted {
		t.Fatalf("Status = %q (err=%v); want completed", out.Status, out.Err)
	}

	entries, err := store.Read("run-file-journal-concurrency")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	logCount := 0
	for _, e := range entries {
		if e.Kind == EntryLog {
			logCount++
		}
	}
	if logCount != 40 {
		t.Fatalf("journal has %d log entries; want 40 — no entry may be lost to a concurrent FileJournalStore.Save race", logCount)
	}
}

// --- Finding 12: currentBranch() silent path="" fallback --------------
//
// No behavioral test: the fallback is unreachable in normal operation
// (Run/parallel/pipeline always seed branchLocalKey before running script
// code). The fix (engine.go's currentBranch) replaced the silent
// &branchState{path:""} fallback — which would let concurrent callers
// collide at the same (Branch="",LocalSeq=0) journal key — with a panic,
// so an internal invariant violation fails loud instead of silently
// corrupting the journal. See the fix-report's finding-12 note.
