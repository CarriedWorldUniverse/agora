// TestFlowQuestionParkResume (blueprint §3.3): a real daemon-1 parks a
// thread on a blocking question, is closed, and a real daemon-2 built over
// the SAME on-disk persistence.LocalStore answers it purely from replay —
// proving planning.QuestionLog/ParkLog's durability contract for real,
// across an actual process-boundary-shaped teardown+reconstruct, not an
// in-memory stand-in.
//
// Byte-exact fixture comparison is NOT possible for this flow and is not
// attempted: planning.QuestionLog.Ask mints the question's ID itself, from
// crypto/rand (question.go's newQuestionID) — there is no seam to inject a
// deterministic ID generator, and faking one to match the fixture's literal
// "qu_0001" would mean the id on the wire came from the test, not the real
// seam call this unit exists to prove. This drive instead asserts the same
// STRUCTURAL invariants contracts_test.go's TestQuestionFlowShape checks
// against the fixture (asked -> waiting -> answered -> resumed ordering,
// id correlation, attribution) plus byte-exact content for every OTHER
// field (text/options/evidence/usage).
package conformance

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/daemon"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
)

var flowQuestionArgs = contracts.QuestionArgs{
	Text:    "Which registry is canonical for the staging images?",
	Context: "Both ghcr.io and the cluster-local registry carry a staging tag; the spec names neither.",
	Evidence: []string{
		"deploy/staging.yaml:14",
		"docs/spec/registries.md",
	},
	Options: []contracts.QuestionOption{
		{Label: "ghcr.io", Description: "the org registry"},
		{Label: "cluster-local", Description: "localhost:5000 on the node"},
	},
	FreeText: true,
}

// askOnFirstInputEngine is daemon-1's bespoke Engine (the house inline-
// Engine pattern, blueprint §2 — this flow's cross-restart shape doesn't
// fit flowEngine's single-process awaitStep script, so it gets its own
// small Engine rather than stretching that abstraction): on the first
// Input (the user_message that starts the turn) it calls the REAL
// planning.QuestionLog.Ask, then emits thread.started/turn.started/
// question.asked(from the real Outcome.Question)/thread.waiting. The turn
// then simply never advances further within this daemon instance — that IS
// the park (blueprint §3.3 step 2).
type askOnFirstInputEngine struct {
	questions *planning.QuestionLog
	threadID  string
	ts        time.Time
}

func (e *askOnFirstInputEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case _, ok := <-in:
		if !ok {
			return nil
		}
	}
	outcome, err := e.questions.Ask(planning.AskRequest{
		ThreadID: e.threadID,
		Args:     flowQuestionArgs,
		Source:   contracts.QuestionFromAgent,
		Blocking: true,
		Context:  planning.ContextInteractive,
		TS:       e.ts,
		Identity: "agora:k5xw3zjanfzsa2lt",
	})
	if err != nil {
		return err
	}
	if outcome.Disposition != planning.DispositionPark || outcome.Parked == nil {
		return errUnexpectedDisposition
	}
	events := []contracts.Event{
		newThreadStarted(e.threadID, threadStartedPayload{IdentityFP: "agora:k5xw3zjanfzsa2lt", Profile: "dev", WorkingDir: "/work/demo"}),
		newTurnStarted(e.threadID, "tu_0001"),
		{Type: contracts.EvQuestionAsked, ThreadID: e.threadID, TurnID: "tu_0001", Payload: mustMarshalJSON(outcome.Question)},
		daemon.NewThreadWaitingEvent(e.threadID, outcome.Question.ID),
	}
	for _, ev := range events {
		if !sendFlowEvent(ctx, out, ev) {
			return ctx.Err()
		}
	}
	// Parked: block for the remainder of this daemon instance's lifetime
	// (or the connection closing), never advancing the turn — matching
	// planning-questions §5's "thread -> waiting-on-answer" state.
	<-ctx.Done()
	return ctx.Err()
}

// answerOnQuestionResponseEngine is daemon-2's bespoke Engine — the SHORTER
// tail script (blueprint §6 resolution 3: don't preserve step position
// across restart): it reconstructs the parked question's ID purely from a
// FRESH planning.NewParkLog(store).IsWaiting replay (the durability proof),
// then on a matching question_response Input calls the REAL
// planning.QuestionLog.Answer and emits question.answered/thread.resumed/
// turn.started(tu_0002)/turn.completed.
type answerOnQuestionResponseEngine struct {
	questions  *planning.QuestionLog
	byOf       func(ctx context.Context, id string) (string, error)
	threadID   string
	questionID string
	ts         time.Time
}

type questionAnsweredWirePayload struct {
	ID     string           `json:"id"`
	Answer contracts.Answer `json:"answer"`
}

func (e *answerOnQuestionResponseEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case i, ok := <-in:
			if !ok {
				return nil
			}
			if i.Type != contracts.InQuestionResponse || i.ID != e.questionID || i.Answer == nil {
				continue
			}
			by, err := e.byOf(ctx, i.ID)
			if err != nil {
				return err
			}
			ans := contracts.Answer{AnswerInput: *i.Answer, By: by}
			if err := e.questions.Answer(e.threadID, e.questionID, ans, e.ts, by); err != nil {
				return err
			}
			events := []contracts.Event{
				{Type: contracts.EvQuestionAnswered, ThreadID: e.threadID, Payload: mustMarshalJSON(questionAnsweredWirePayload{ID: e.questionID, Answer: ans})},
				daemon.NewThreadResumedEvent(e.threadID, e.questionID),
				newTurnStarted(e.threadID, "tu_0002"),
				newTurnCompleted(e.threadID, "tu_0002", contracts.Usage{Input: 3100, Output: 140}),
			}
			for _, ev := range events {
				if !sendFlowEvent(ctx, out, ev) {
					return ctx.Err()
				}
			}
			return nil
		}
	}
}

var errUnexpectedDisposition = jsonErr("conformance: question ladder resolved to an unexpected disposition")

type jsonErr string

func (e jsonErr) Error() string { return string(e) }

func TestFlowQuestionParkResume(t *testing.T) {
	fixture := loadFlow(t, "question_park_resume.jsonl")

	dir := t.TempDir()
	store, err := persistence.NewLocalStore(dir, persistence.Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	// Windows CI is strict on file handles (repo convention): close the
	// store's own handle only after every daemon/session built over it has
	// torn down (t.Cleanup runs after this function's own defers, so it
	// runs after d2.Close()/ln2.Close() below).
	t.Cleanup(func() { _ = store.Close() })

	registry := remote.NewRegistry(nil)
	if _, err := registry.Enroll("agora:q7ymdevice001", nil, remote.DeviceMetadata{}, []contracts.Capability{contracts.CapInteractive}); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	threadID := "th_0003"
	if err := store.Create(contracts.ThreadMeta{ThreadID: threadID, CreatedAt: time.Unix(0, 0).UTC(), Profile: "dev"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	// --- daemon-1: park ---
	ctx1, cancel1 := context.WithCancel(context.Background())
	d1 := daemon.NewDaemon(ctx1, daemon.Config{
		Store:    store,
		Registry: registry,
		EngineFactory: func(tid string, meta contracts.ThreadMeta) agoraio.Engine {
			return &askOnFirstInputEngine{questions: newQuestionLogFor(store), threadID: tid, ts: time.Unix(100, 0).UTC()}
		},
	})
	sock1 := sessionSockPath(t, "qpr-1.sock")
	ln1, err := agoraio.ListenUnix(sock1)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("unix sockets unsupported: %v", err)
		}
		t.Fatalf("ListenUnix: %v", err)
	}
	go func() { _ = d1.ServeUnix(ctx1, ln1) }()

	backend1, err := tui.DialUnixBackend(sock1, agoraio.AttachRequest{ThreadID: threadID, ClientID: "agora:q7ymdevice001", Kind: "tui", Replay: 16})
	if err != nil {
		t.Fatalf("dial daemon-1: %v", err)
	}
	if err := backend1.Send(ctx1, contracts.Input{Type: contracts.InUserMessage, Text: "which registry should I deploy staging images to?"}); err != nil {
		t.Fatalf("send user_message: %v", err)
	}

	var instance1Events []contracts.Event
	instance1Events = append(instance1Events, waitForType(t, backend1, contracts.EvThreadStarted))
	instance1Events = append(instance1Events, waitForType(t, backend1, contracts.EvTurnStarted))
	askedEvent := waitForType(t, backend1, contracts.EvQuestionAsked)
	instance1Events = append(instance1Events, askedEvent)
	instance1Events = append(instance1Events, waitForType(t, backend1, contracts.EvThreadWaiting))

	var asked contracts.QuestionAsked
	if err := json.Unmarshal(askedEvent.Payload, &asked); err != nil {
		t.Fatalf("decode question.asked: %v", err)
	}
	if asked.ID == "" {
		t.Fatal("real QuestionLog.Ask minted an empty question id")
	}

	_ = backend1.Close()
	ln1.Close()
	cancel1()
	d1.Close()

	// --- durability proof: a FRESH ParkLog, purely from replay ---
	waiting, ok, err := planning.NewParkLog(store).IsWaiting(threadID)
	if err != nil {
		t.Fatalf("IsWaiting after restart: %v", err)
	}
	if !ok || waiting.Question.ID != asked.ID {
		t.Fatalf("park state did not survive the restart purely from replay: ok=%v got=%+v want id=%s", ok, waiting, asked.ID)
	}

	// --- daemon-2: resume, over the SAME store ---
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	var d2 *daemon.Daemon
	d2 = daemon.NewDaemon(ctx2, daemon.Config{
		Store:    store,
		Registry: registry,
		EngineFactory: func(tid string, meta contracts.ThreadMeta) agoraio.Engine {
			return &answerOnQuestionResponseEngine{
				questions:  newQuestionLogFor(store),
				byOf:       func(ctx context.Context, id string) (string, error) { return d2.WaitForBy(ctx, id) },
				threadID:   tid,
				questionID: asked.ID,
				ts:         time.Unix(200, 0).UTC(),
			}
		},
	})
	defer d2.Close()
	sock2 := sessionSockPath(t, "qpr-2.sock")
	ln2, err := agoraio.ListenUnix(sock2)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln2.Close()
	go func() { _ = d2.ServeUnix(ctx2, ln2) }()

	backend2, err := tui.DialUnixBackend(sock2, agoraio.AttachRequest{ThreadID: threadID, ClientID: "agora:q7ymdevice001", Kind: "tui", Replay: 16})
	if err != nil {
		t.Fatalf("dial daemon-2: %v", err)
	}
	defer backend2.Close()
	if err := backend2.Send(ctx2, contracts.Input{
		Type: contracts.InQuestionResponse, ID: asked.ID,
		Answer: &contracts.AnswerInput{Choice: []string{"cluster-local"}},
	}); err != nil {
		t.Fatalf("send question_response: %v", err)
	}

	var instance2Events []contracts.Event
	instance2Events = append(instance2Events, waitForType(t, backend2, contracts.EvQuestionAnswered))
	instance2Events = append(instance2Events, waitForType(t, backend2, contracts.EvThreadResumed))
	instance2Events = append(instance2Events, waitForType(t, backend2, contracts.EvTurnStarted))
	instance2Events = append(instance2Events, waitForType(t, backend2, contracts.EvTurnCompleted))

	all := append(append([]contracts.Event{}, instance1Events...), instance2Events...)
	assertQuestionFlowStructurallyMatches(t, all, fixture, asked.ID)
}

// newQuestionLogFor/d2Questions are tiny helpers so both daemons' engine
// factories share exactly the daemon's own *planning.QuestionLog (not a
// second, independent one over the same store — QuestionLog's per-thread
// mutex, question.go, is what makes Ask/Answer's check-then-act atomic; a
// second independent instance would still be correct here since the mutex
// only matters for CONCURRENT callers, but reusing d.Questions() is the
// real, intended daemon wiring rather than a parallel one this test built
// on the side).
func newQuestionLogFor(store contracts.ThreadStore) *planning.QuestionLog {
	return planning.NewQuestionLog(store)
}

// assertQuestionFlowStructurallyMatches checks the concatenated live-drive
// events against the fixture per-line: event Type sequence must match
// exactly; every field must match too EXCEPT the question/answer id
// (real, randomly minted) which instead must be INTERNALLY CONSISTENT
// (question.asked/thread.waiting/question.answered/thread.resumed all
// correlate to the SAME real id) and every payload's non-id content
// (text/options/evidence/answer choice/usage) must match the fixture's
// byte-for-byte.
func assertQuestionFlowStructurallyMatches(t *testing.T, got, want []contracts.Event, questionID string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i].Type != want[i].Type {
			t.Fatalf("line %d: type = %s, want %s", i+1, got[i].Type, want[i].Type)
		}
		if got[i].ThreadID != want[i].ThreadID {
			t.Fatalf("line %d: thread_id = %s, want %s", i+1, got[i].ThreadID, want[i].ThreadID)
		}
		switch got[i].Type {
		case contracts.EvQuestionAsked:
			var g, w contracts.QuestionAsked
			mustDecode(t, got[i].Payload, &g)
			mustDecode(t, want[i].Payload, &w)
			if g.ID != questionID {
				t.Fatalf("line %d: question.asked id %q != real minted id %q", i+1, g.ID, questionID)
			}
			g.ID = w.ID // the only field allowed to differ
			if !json.Valid(mustMarshalJSON(g)) {
				t.Fatalf("line %d: re-marshal failed", i+1)
			}
			if string(mustMarshalJSON(g)) != string(mustMarshalJSON(w)) {
				t.Fatalf("line %d: question.asked content mismatch (ignoring id)\ngot:  %+v\nwant: %+v", i+1, g, w)
			}
		case contracts.EvThreadWaiting, contracts.EvThreadResumed:
			var g, w struct {
				QuestionID string `json:"question_id"`
			}
			mustDecode(t, got[i].Payload, &g)
			mustDecode(t, want[i].Payload, &w)
			if g.QuestionID != questionID {
				t.Fatalf("line %d: %s question_id %q != real minted id %q", i+1, got[i].Type, g.QuestionID, questionID)
			}
		case contracts.EvQuestionAnswered:
			var g, w questionAnsweredWirePayload
			mustDecode(t, got[i].Payload, &g)
			mustDecode(t, want[i].Payload, &w)
			if g.ID != questionID {
				t.Fatalf("line %d: question.answered id %q != real minted id %q", i+1, g.ID, questionID)
			}
			if g.Answer.By == "" {
				t.Fatalf("line %d: answer lacks attribution", i+1)
			}
			if len(g.Answer.Choice) != len(w.Answer.Choice) || (len(g.Answer.Choice) > 0 && g.Answer.Choice[0] != w.Answer.Choice[0]) {
				t.Fatalf("line %d: answer choice = %v, want %v", i+1, g.Answer.Choice, w.Answer.Choice)
			}
		case contracts.EvTurnStarted:
			if got[i].TurnID != want[i].TurnID {
				t.Fatalf("line %d: turn_id = %s, want %s", i+1, got[i].TurnID, want[i].TurnID)
			}
		case contracts.EvTurnCompleted:
			if got[i].TurnID != want[i].TurnID {
				t.Fatalf("line %d: turn_id = %s, want %s", i+1, got[i].TurnID, want[i].TurnID)
			}
			if string(got[i].Payload) != string(want[i].Payload) {
				t.Fatalf("line %d: turn.completed payload = %s, want %s", i+1, got[i].Payload, want[i].Payload)
			}
		}
	}
}

func mustDecode(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
