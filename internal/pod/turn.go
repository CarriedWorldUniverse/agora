package pod

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
)

// TurnResult is what RunTurn reports: either the turn ran to completion
// (Events holds whatever the engine emitted, in order) or it hit a blocking
// question and the pod terminated it honestly (Blocked is set, Events holds
// only what was emitted before the conversion). Exactly one of Events being
// non-empty in the completed sense / Blocked being non-nil describes the
// outcome — Blocked is the authoritative field to check.
type TurnResult struct {
	Events  []contracts.Event
	Blocked *contracts.BlockedNeedsInput
}

// RunTurn drives one turn on the pod's provisioned session as its dispatch
// controller (§6a: "dispatch drives it like any interactive client:
// user_message = the ticket/task; events stream back"). Refuses with
// ErrNotProvisioned while blank (§6a: "refuses turns until provisioned").
//
// If the engine raises a blocking question (question.asked, blocking=true)
// mid-turn, RunTurn performs the harness-side ladder conversion instead of
// forwarding it as an interactive card: planning-questions §5's dispatch
// row is "die honestly: terminate with typed result blocked: needs-input
// {question}; lease releases immediately — no sleeping pod holds a work
// item." internal/planning.Resolve(ContextDispatchPod, blocking) always
// yields DispositionDieHonestly for a blocking question — there is no
// interactive human to park on inside a pod — so this method uses
// planning.QuestionLog.Ask (Context: ContextDispatchPod) to get that
// disposition AND its durable audit trail (the TIQuestionAsked item, same
// as the interactive path), then returns TurnResult.Blocked instead of
// continuing to read the (now-halted) turn. The pod itself is untouched —
// only the turn/work-item is terminated (doc.go's warm-pool note).
func (p *Pod) RunTurn(ctx context.Context, text string) (TurnResult, error) {
	p.mu.Lock()
	if p.state != StateProvisioned {
		p.mu.Unlock()
		return TurnResult{}, ErrNotProvisioned
	}
	threadID := p.info.ThreadID
	identityFP := p.info.IdentityFP
	attach := p.attach
	p.mu.Unlock()

	if err := attach.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: text}); err != nil {
		return TurnResult{}, fmt.Errorf("pod: send user_message: %w", err)
	}

	var result TurnResult
	events := attach.Events()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return result, nil
			}

			if ev.Type == contracts.EvQuestionAsked {
				var q contracts.QuestionAsked
				if err := json.Unmarshal(ev.Payload, &q); err != nil {
					return TurnResult{}, fmt.Errorf("pod: decode question.asked: %w", err)
				}
				if q.Blocking {
					outcome, err := p.questions.Ask(planning.AskRequest{
						ThreadID: threadID,
						Args:     q.Args,
						Source:   q.Source,
						Blocking: true,
						Context:  planning.ContextDispatchPod,
						TS:       p.clock(),
						Identity: identityFP,
					})
					if err != nil {
						return TurnResult{}, fmt.Errorf("pod: ladder resolution: %w", err)
					}
					if outcome.Disposition != planning.DispositionDieHonestly || outcome.Terminate == nil {
						// Never reached in practice — ContextDispatchPod always
						// resolves blocking to die-honestly (planning.Resolve) —
						// but fail closed rather than silently forwarding the
						// model past an unanswered question if that invariant
						// ever breaks.
						return TurnResult{}, fmt.Errorf("pod: unexpected disposition %q for a blocking question in a dispatch pod", outcome.Disposition)
					}
					return TurnResult{Events: result.Events, Blocked: outcome.Terminate}, nil
				}
				// blocking:false questions queue and the turn continues (§5) —
				// fall through and record the event like any other.
			}

			result.Events = append(result.Events, ev)

			if ev.Type == contracts.EvTurnCompleted || ev.Type == contracts.EvTurnFailed {
				return result, nil
			}
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		}
	}
}
