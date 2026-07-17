package workflow

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
)

// QuestionRouter is the seam ctx.question/ctx.approval route through —
// U11's escalation ladder + park machinery (internal/planning), NOT a
// bespoke path (build brief). Log does the actual Ask/Answer work; Store is
// the SAME contracts.ThreadStore Log was built over, used here read-only to
// look up whether a previously-parked question has since been answered
// (planning.QuestionLog exposes no such query itself — this replays the
// thread's own persisted items the same way planning.ParkLog.IsWaiting
// does, reading the public contracts.ThreadItem/ThreadStore contract rather
// than adding new machinery).
type QuestionRouter struct {
	Log   *planning.QuestionLog
	Store contracts.ThreadStore
}

// questionAnsweredWire mirrors planning's private questionAnsweredPayload
// wire shape (json tags "question_id"/"answer") — decoding the public
// contracts.ThreadItem payload of a TIQuestionAnswered item, not coupling
// to planning's unexported type.
type questionAnsweredWire struct {
	QuestionID string           `json:"question_id"`
	Answer     contracts.Answer `json:"answer"`
}

// lookupAnswer replays threadID looking for a TIQuestionAnswered item
// matching questionID — used on resume to check whether a run parked on an
// unanswered question has, since parking, actually been answered (spec §2:
// "a daemon restart mid-question replays to the unanswered call and
// re-raises it" is the fallback; this is the path that lets a resume pick
// up an answer that arrived while the run was parked instead of
// re-parking).
func (r *QuestionRouter) lookupAnswer(threadID, questionID string) (contracts.Answer, bool, error) {
	it, err := r.Store.Resume(threadID)
	if err != nil {
		return contracts.Answer{}, false, fmt.Errorf("workflow: replay thread for answer lookup: %w", err)
	}
	defer it.Close()

	var found *contracts.Answer
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type != contracts.TIQuestionAnswered {
			continue
		}
		b, err := json.Marshal(item.Payload)
		if err != nil {
			return contracts.Answer{}, false, fmt.Errorf("workflow: re-marshal question_answered payload: %w", err)
		}
		var w questionAnsweredWire
		if err := json.Unmarshal(b, &w); err != nil {
			return contracts.Answer{}, false, fmt.Errorf("workflow: decode question_answered payload: %w", err)
		}
		if w.QuestionID == questionID {
			ans := w.Answer
			found = &ans
		}
	}
	if err := it.Err(); err != nil {
		return contracts.Answer{}, false, fmt.Errorf("workflow: replay error during answer lookup: %w", err)
	}
	if found == nil {
		return contracts.Answer{}, false, nil
	}
	return *found, true, nil
}

// askPayload is the decoded shape of ctx.question's `payload` starlark
// dict / ctx.approval's msg+payload. Field names chosen to mirror
// contracts.QuestionArgs directly (a resolved ambiguity: the spec names
// the parameter `payload` but does not enumerate its keys — see the build
// report).
type askPayload struct {
	Text        string   `json:"text"`
	Context     string   `json:"context,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	Options     []option `json:"options,omitempty"`
	MultiSelect bool     `json:"multi_select,omitempty"`
	FreeText    bool     `json:"free_text,omitempty"`
}

type option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func (p askPayload) toQuestionArgs() contracts.QuestionArgs {
	opts := make([]contracts.QuestionOption, len(p.Options))
	for i, o := range p.Options {
		opts[i] = contracts.QuestionOption{Label: o.Label, Description: o.Description}
	}
	return contracts.QuestionArgs{
		Text:        p.Text,
		Context:     p.Context,
		Evidence:    p.Evidence,
		Options:     opts,
		MultiSelect: p.MultiSelect,
		FreeText:    p.FreeText,
	}
}

// approvalOptions is the fixed two-option shape ctx.approval encodes as a
// question — spec §2: "Same pipeline as ctx.question, one implementation,
// two verbs." Resolved ambiguity: approval reuses QuestionArgs verbatim
// with these two labels; the returned bool is Choice[0] == approvalAllow.
var approvalOptions = []contracts.QuestionOption{
	{Label: approvalAllow},
	{Label: approvalDeny},
}

const (
	approvalAllow = "Allow"
	approvalDeny  = "Deny"
)

func approvalArgs(msg string, extra askPayload) contracts.QuestionArgs {
	qa := extra.toQuestionArgs()
	qa.Text = msg
	qa.Options = approvalOptions
	qa.FreeText = false
	qa.MultiSelect = false
	return qa
}

func decisionFromAnswer(ans contracts.Answer) bool {
	for _, c := range ans.Choice {
		if c == approvalAllow {
			return true
		}
	}
	return false
}

// askContext is the QuestionContext a workflow run's OWN ctx.question/
// ctx.approval calls raise under. Resolved ambiguity: the spec's ladder
// (agora-spec-planning-questions.md §5) has no dedicated "workflow run"
// row; a workflow run is, like an orchestrator thread, a background object
// with a human inbox behind it (spec §2: "the RUN parks ... runs are
// background objects already"), so it takes ContextOrchestrator's row —
// which parks, matching spec §2's stated behavior exactly. A question
// BUBBLED UP from an agent spawned inside a stage (ctx.agent's Question
// result) also lands here once it reaches the engine (spec §2: "else the
// run parks") — v1 has no nested-workflow bubbling target above the
// engine itself (ctx.workflow nesting is deferred per spec §7), so
// "bubbles to the engine" and "the engine parks" collapse to the same
// disposition.
const askContext = planning.ContextOrchestrator

// Ask raises q under askContext, always blocking (spec §2: "Blocking by
// construction").
func (r *QuestionRouter) Ask(threadID, identity string, args contracts.QuestionArgs, source contracts.QuestionSource, ts time.Time) (planning.Outcome, error) {
	return r.Log.Ask(planning.AskRequest{
		ThreadID: threadID,
		Args:     args,
		Source:   source,
		Blocking: true,
		Context:  askContext,
		TS:       ts,
		Identity: identity,
	})
}
