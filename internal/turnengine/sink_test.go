package turnengine

import (
	"context"
	"encoding/json"
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
	s := newTurnSink("th", "tu", out, context.Background(), nil)
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
		// These used to emit NOTHING. Silence was wrong for the resume
		// fallback: the turn quietly restarts on a fresh provider session
		// and the prior context is gone, with nothing in the stream saying
		// so (agora#120). They are notes now — still never errors.
		if len(got) != 1 {
			t.Errorf("stage %q emitted %d events; want exactly 1 (a note)", stage, len(got))
			continue
		}
		if got[0].Type == contracts.EvError {
			t.Errorf("stage %q emitted an EvError; non-terminal stages must never render as a terminal error", stage)
		}
		if got[0].Type != contracts.EvWarning {
			t.Errorf("stage %q emitted %q; want %q", stage, got[0].Type, contracts.EvWarning)
		}
		var p contracts.WarningPayload
		if err := json.Unmarshal(got[0].Payload, &p); err != nil {
			t.Errorf("stage %q: decode payload: %v", stage, err)
			continue
		}
		if p.Message != "noise" {
			t.Errorf("stage %q: message = %q; want the underlying error text", stage, p.Message)
		}
		if p.Stage != string(stage) {
			t.Errorf("stage %q: payload stage = %q; want the stage carried verbatim so consumers can filter by cause", stage, p.Stage)
		}
	}

	got := emitAndCollect(t, bridle.Warning{Kind: "k", Message: "m"})
	if len(got) != 1 || got[0].Type != contracts.EvWarning {
		t.Fatalf("bridle.Warning emitted %d events (%v); want exactly 1 EvWarning", len(got), eventTypes(got))
	}

	// A warning with no message is noise, not information.
	if empty := emitAndCollect(t, bridle.Warning{Kind: "k"}); len(empty) != 0 {
		t.Errorf("an empty bridle.Warning emitted %d events; want 0", len(empty))
	}
	if empty := emitAndCollect(t, bridle.TurnError{Stage: bridle.TurnErrorStageRetry}); len(empty) != 0 {
		t.Errorf("a nil-Err TurnError emitted %d events; want 0", len(empty))
	}
}

func eventTypes(evs []contracts.Event) []contracts.EventType {
	out := make([]contracts.EventType, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
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

// TestSink_AgentCallEmitsAgentSpawnItem pins the wire half of agora#155: an
// agent() call must be distinguishable on the stream, not folded into the
// generic command_execution bucket. Without a distinct item type no consumer
// — TUI, pipe, or a daemon client — can tell a subagent is running, which is
// how a deadlocked child stayed invisible for 30+ minutes.
func TestSink_AgentCallEmitsAgentSpawnItem(t *testing.T) {
	if got := itemTypeForTool("agent"); got != contracts.ItemAgentSpawn {
		t.Fatalf("itemTypeForTool(agent) = %q; want %q", got, contracts.ItemAgentSpawn)
	}
	// The payload must name the agent type, since that is what a display
	// shows, and must default rather than render empty.
	if got := agentSpawnSummary(json.RawMessage(`{"agent_type":"reviewer","prompt":"x"}`)); got != "reviewer" {
		t.Errorf("agentSpawnSummary = %q; want \"reviewer\"", got)
	}
	if got := agentSpawnSummary(json.RawMessage(`{"prompt":"x"}`)); got != "general-purpose" {
		t.Errorf("agentSpawnSummary with no agent_type = %q; want the toolrunner default", got)
	}
	if got := agentSpawnSummary(json.RawMessage(`not json`)); got != "general-purpose" {
		t.Errorf("agentSpawnSummary on malformed args = %q; want the default, never empty", got)
	}
	// And the other tools must be unaffected.
	if got := itemTypeForTool("run_command"); got != contracts.ItemCommandExecution {
		t.Errorf("run_command regressed to %q", got)
	}
}
