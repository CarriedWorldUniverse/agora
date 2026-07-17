package io

import (
	"context"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Engine drives one thread's turns — the minimal seam this package needs
// from the turn engine. The real implementation (model calls, tool loop,
// context assembly, approvals) is a later build unit; io only needs
// "consume Input, produce Event" to route it to pipe mode or a Session's
// fan-out.
//
// Run owns one thread end-to-end: it consumes Input from in until in is
// closed or ctx is canceled, and emits Event to out for the caller to
// forward (RunPipe writes them to stdout; Session broadcasts them to every
// attached client). Run MUST close out before returning — callers select on
// out closing to know the engine is done. A user_message starts a turn;
// steer/interrupt/approval_response/question_response/config affect the
// current or next turn; end is a polite request to wind down (Run may also
// just return when in closes, per §1 "graceful shutdown ... also stdin
// EOF").
//
// Spec: agora-spec-io.md §1 (the Input/Event shapes Engine must honor).
type Engine interface {
	Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error
}
