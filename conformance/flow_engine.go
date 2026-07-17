// flow_engine.go: the conformance driver Engine. Lives in package
// conformance (test-only), NOT internal/io — putting it in io would pull
// approval/planning into io's import graph, backwards from io's role as
// the low-level wire seam (blueprint §2). It follows the house
// inline-Engine pattern (io/session_test.go's echoEngine,
// pod/pod.go's provisionedEngine): pre-can only the model-originated
// TRIGGER content (Emit) and delegate every RESOLUTION to a real seam call
// (Resolve) — that's what makes a flow's golden output PROVE the real
// internal/approval, internal/planning, internal/ctxmgr, internal/pod
// seams produced it, not a pre-baked script (blueprint §0).
package conformance

import (
	"context"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// awaitStep is one step of a flowEngine script: Emit fires the moment the
// step is reached; if Await is non-empty the engine then blocks for a
// matching Input (by Type, and by ID when AwaitID is set) before advancing,
// calling Resolve — the REAL seam call — and splicing whatever events it
// returns into the stream. Await == "" advances immediately with no block
// (a pure-emit step).
type awaitStep struct {
	Emit    []contracts.Event
	Await   contracts.InputType
	AwaitID string
	Resolve func(in contracts.Input) ([]contracts.Event, error)
}

// flowEngine drives a []awaitStep script. The first Input's own content is
// never inspected for a step with Await == "" (matching ScriptedEngine's
// convention, scripted_engine.go: "the Nth Input read produces Script[N]'s
// Events, regardless of the Input's own content") — only Await steps
// actually match on Input shape.
type flowEngine struct {
	steps []awaitStep
}

var _ agoraio.Engine = (*flowEngine)(nil)

// errUnscriptedPayload mirrors io.errorPayload's shape (unexported there),
// so a flowEngine driven past its script produces a well-formed error
// event instead of hanging or panicking, same as ScriptedEngine.
type errUnscriptedPayload struct {
	Message string `json:"message"`
}

func (e *flowEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	defer close(out)
	idx := 0
outer:
	for {
		for idx < len(e.steps) {
			step := e.steps[idx]
			for _, ev := range step.Emit {
				if !sendFlowEvent(ctx, out, ev) {
					return ctx.Err()
				}
			}
			if step.Await == "" {
				idx++
				continue
			}
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case inp, ok := <-in:
					if !ok {
						return nil
					}
					if inp.Type != step.Await || (step.AwaitID != "" && inp.ID != step.AwaitID) {
						continue // non-matching input: ignored, keep waiting
					}
					if step.Resolve != nil {
						evs, err := step.Resolve(inp)
						if err != nil {
							return err
						}
						for _, ev := range evs {
							if !sendFlowEvent(ctx, out, ev) {
								return ctx.Err()
							}
						}
					}
					idx++
					continue outer
				}
			}
		}
		// Script exhausted: drain further input, emitting EvError for each
		// (mirrors io.ScriptedEngine.ErrUnscripted — a stub used outside its
		// scripted scenario still produces a well-formed stream).
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-in:
			if !ok {
				return nil
			}
			ev := mustFlowEvent(contracts.EvError, errUnscriptedPayload{Message: "flow_engine: unscripted input"})
			if !sendFlowEvent(ctx, out, ev) {
				return ctx.Err()
			}
		}
	}
}

func sendFlowEvent(ctx context.Context, out chan<- contracts.Event, ev contracts.Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
