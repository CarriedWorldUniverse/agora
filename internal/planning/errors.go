package planning

import "errors"

// Sentinel errors, so callers can errors.Is regardless of which function
// produced them. Spec: agora-spec-planning-questions.md.
var (
	// ErrUnknownContext is returned by Resolve for a QuestionContext outside
	// the ladder's recognized set (§5). Fail-safe: an unrecognized context
	// is never guessed at — it errors rather than silently picking a
	// disposition, mirroring internal/approval's fail-closed-on-unknown-kind
	// discipline.
	ErrUnknownContext = errors.New("planning: unrecognized question context")

	// ErrOpenQuestions is returned when a plan gate allow is attempted while
	// open_questions is non-empty — invariant 6: the posture NEVER exits
	// with open questions, even on an explicit operator exit.
	ErrOpenQuestions = errors.New("planning: plan gate cannot allow exit while open_questions is non-empty")

	// ErrPlanNotSubmitted is returned when Gate is called on a plan artifact
	// that never set Submit=true — submitting is what raises the KindPlan
	// approval in the first place (§3).
	ErrPlanNotSubmitted = errors.New("planning: plan gate requires submit=true")

	// ErrUnknownExit is returned for a PlanExit outside {inline, delegate}.
	ErrUnknownExit = errors.New("planning: unrecognized plan exit")

	// ErrUnknownDecision is returned for a contracts.Decision outside
	// {allow, deny} on a gate request — fail closed, never guess.
	ErrUnknownDecision = errors.New("planning: unrecognized gate decision")

	// ErrNotWaiting is returned when Answer/Resume references a question the
	// thread is not currently parked on (a stray/late answer, or one that
	// already resumed).
	ErrNotWaiting = errors.New("planning: thread is not parked waiting on this question")

	// ErrAlreadyParked is returned by Ask when the thread is already parked
	// on an unresolved blocking question — one blocking question per thread
	// (§4 "one thing at a time"); parking a second would orphan the first.
	ErrAlreadyParked = errors.New("planning: thread already parked on an unresolved question")

	// ErrUnattributedAnswer guards the never-fabricate boundary at the API
	// surface: an Answer's By must be set by the caller from the
	// authenticated connection identity, never left blank (contracts.Answer,
	// §6 invariant 4).
	ErrUnattributedAnswer = errors.New("planning: answer must carry an attributed by identity")
)
