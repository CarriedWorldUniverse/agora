package turnengine

import (
	"context"
	"errors"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// emitAndCollect feeds one bridle event through a fresh turnSink and returns
// whatever it forwarded (non-blocking drain of a generously buffered channel).
func emitAndCollect(t *testing.T, e bridle.Event) []contracts.Event {
	t.Helper()
	out := make(chan contracts.Event, 8)
	s := newTurnSink("th", "tu", out, context.Background())
	s.Emit(e)
	close(out)
	var got []contracts.Event
	for ev := range out {
		got = append(got, ev)
	}
	return got
}

// TestSink_NonTerminalStages_NotErrors: bridle's informational TurnError
// stages (and bridle.Warning) must NOT surface as agora's terminal "error:"
// status — the first live turn failed exactly here, a benign sidecar-stderr
// Node warning masquerading as a turn failure.
func TestSink_NonTerminalStages_NotErrors(t *testing.T) {
	nonTerminal := []bridle.TurnErrorStage{
		bridle.TurnErrorStageRetry,
		bridle.TurnErrorStageProviderAPIError,
		bridle.TurnErrorStageResumeFallback,
	}
	for _, stage := range nonTerminal {
		got := emitAndCollect(t, bridle.TurnError{Err: errors.New("noise"), Stage: stage})
		if len(got) != 0 {
			t.Errorf("stage %q emitted %d events; want 0 (non-terminal, must not be an error)", stage, len(got))
		}
	}

	if got := emitAndCollect(t, bridle.Warning{Kind: "k", Message: "m"}); len(got) != 0 {
		t.Errorf("bridle.Warning emitted %d events; want 0", len(got))
	}
}

// TestSink_TerminalStages_AreErrors: real failures still surface as EvError so
// nothing terminal is hidden.
func TestSink_TerminalStages_AreErrors(t *testing.T) {
	terminal := []bridle.TurnErrorStage{
		bridle.TurnErrorStageProvider,
		bridle.TurnErrorStageHarnessRecover,
		bridle.TurnErrorStageSubprocessExit,
		bridle.TurnErrorStageStreamTruncated,
		// StderrOutput carries arbitrary sidecar stderr (incl. real failures
		// like a missing-creds auth error) — it must stay visible, not be
		// swallowed as a benign warning.
		bridle.TurnErrorStageStderrOutput,
	}
	for _, stage := range terminal {
		got := emitAndCollect(t, bridle.TurnError{Err: errors.New("boom"), Stage: stage})
		if len(got) != 1 || got[0].Type != contracts.EvError {
			t.Errorf("stage %q emitted %+v; want exactly one EvError", stage, got)
		}
	}
}
