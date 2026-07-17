package planning

import (
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// QuestionContext identifies where in the execution topology a `question`
// tool call originates — the axis the escalation ladder resolves on.
// Missing information travels UP one level; answers travel back DOWN as
// context — never as a live wire into a sleeping process.
// Spec: agora-spec-planning-questions.md §5.
type QuestionContext string

const (
	// ContextInteractive: an interactive thread with a human at the other
	// end. Ladder row "interactive / orchestrator thread".
	ContextInteractive QuestionContext = "interactive"
	// ContextOrchestrator: an orchestrator thread — shares the interactive
	// row's disposition (park); it is still a thread with a human inbox
	// behind it, just possibly asynchronous.
	ContextOrchestrator QuestionContext = "orchestrator"
	// ContextDispatchPod: a headless one-shot dispatch job. Ladder row
	// "dispatch job (headless pod)".
	ContextDispatchPod QuestionContext = "dispatch_pod"
	// ContextSubagent: a spawned subagent child. Ladder row "subagent /
	// workflow child".
	ContextSubagent QuestionContext = "subagent"
	// ContextWorkflowChild: a workflow-engine child stage. Shares the
	// subagent row's disposition (bubble).
	ContextWorkflowChild QuestionContext = "workflow_child"
)

// Disposition is what the ladder resolves a `question` tool call to.
// Spec: agora-spec-planning-questions.md §5.
type Disposition string

const (
	// DispositionPark: thread → waiting-on-answer (durable, survives daemon
	// restart + detach); the question becomes a queued card.
	DispositionPark Disposition = "park"
	// DispositionDieHonestly: terminate with blocked: needs-input; the lease
	// releases immediately — no sleeping pod holds a work item.
	DispositionDieHonestly Disposition = "die_honestly"
	// DispositionBubble: the question surfaces to the parent through the
	// agent/workflow result channel.
	DispositionBubble Disposition = "bubble"
	// DispositionQueue: blocking:false skips straight to the queue at EVERY
	// level (§5) — the thread/job continues other work; the answer arrives
	// as a later inbox item.
	DispositionQueue Disposition = "queue"
)

// Resolve maps (ctx, blocking) to the disposition the escalation ladder
// prescribes (§5's table, plus the blocking:false row that applies at every
// level). An unrecognized context fails closed with ErrUnknownContext
// rather than guessing a disposition — never-fabricate applies to routing
// decisions too, not just answers.
func Resolve(ctx QuestionContext, blocking bool) (Disposition, error) {
	if !blocking {
		switch ctx {
		case ContextInteractive, ContextOrchestrator, ContextDispatchPod, ContextSubagent, ContextWorkflowChild:
			return DispositionQueue, nil
		default:
			return "", fmt.Errorf("%w: %q", ErrUnknownContext, ctx)
		}
	}

	switch ctx {
	case ContextInteractive, ContextOrchestrator:
		return DispositionPark, nil
	case ContextDispatchPod:
		return DispositionDieHonestly, nil
	case ContextSubagent, ContextWorkflowChild:
		return DispositionBubble, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownContext, ctx)
	}
}

// Terminate builds the one-shot pod termination shape for a blocking
// question raised in ContextDispatchPod: the job dies honestly instead of
// parking, and the dispatcher (orchestrator or operator inbox) re-dispatches
// with the answer folded into the brief. This is the "pod call ⇒ blocked:
// needs-input" golden shape — building the value is all this unit does; the
// actual pod termination/re-dispatch mechanics are the nexus-side dispatch
// contract (§8), a later/other unit.
// Spec: agora-spec-planning-questions.md §5 (dispatch row), §8.
func Terminate(q contracts.QuestionAsked, threadID string) contracts.BlockedNeedsInput {
	return contracts.BlockedNeedsInput{Question: q, ThreadID: threadID}
}
