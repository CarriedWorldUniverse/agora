package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func warnEvent(msg, stage string) contracts.Event {
	b, _ := json.Marshal(contracts.WarningPayload{Message: msg, Stage: stage})
	return contracts.Event{Type: contracts.EvWarning, Payload: b}
}

func errEvent(msg string) contracts.Event {
	b, _ := json.Marshal(struct {
		Message string `json:"message"`
	}{msg})
	return contracts.Event{Type: contracts.EvError, Payload: b}
}

// A resume fallback means the prior provider-side context is gone. The turn
// still succeeds, so the status row is the only place that can say so.
func TestTUI_Warning_ShowsAsANote(t *testing.T) {
	m := NewModel(Config{AgentID: "a", Theme: PlainTheme(), ModelRegistry: testRegistry()})
	m.handleEvent(warnEvent("resume of session \"abc\" failed; prior context is lost", "resume_fallback"))

	row := m.renderStatusRow()
	if !strings.Contains(row, "note:") {
		t.Fatalf("status row = %q; want it to carry the note", row)
	}
	if !strings.Contains(row, "prior context is lost") {
		t.Fatalf("status row = %q; want the warning message", row)
	}
	if strings.Contains(row, "error:") {
		t.Error("a non-terminal note rendered as a terminal error")
	}
}

// A real error must never be masked by a note.
func TestTUI_Error_TakesPrecedenceOverNote(t *testing.T) {
	m := NewModel(Config{AgentID: "a", Theme: PlainTheme(), ModelRegistry: testRegistry()})
	m.handleEvent(warnEvent("benign note", "retry"))
	m.handleEvent(errEvent("the real failure"))

	row := m.renderStatusRow()
	if !strings.Contains(row, "error: the real failure") {
		t.Fatalf("status row = %q; want the error to win", row)
	}
	if strings.Contains(row, "benign note") {
		t.Error("a note masked a real error")
	}
}

// A note describes the turn that produced it; carrying it forward would
// misattribute it to the next turn.
func TestTUI_Note_ClearedOnNextTurn(t *testing.T) {
	m := NewModel(Config{AgentID: "a", Theme: PlainTheme(), ModelRegistry: testRegistry()})
	m.handleEvent(warnEvent("stale note", "resume_fallback"))
	if !strings.Contains(m.renderStatusRow(), "stale note") {
		t.Fatal("precondition: the note should be showing")
	}

	m.handleEvent(contracts.Event{Type: contracts.EvTurnStarted})
	m.running = false // back to idle so the status row renders the note slot
	if row := m.renderStatusRow(); strings.Contains(row, "stale note") {
		t.Fatalf("status row = %q; a note from the previous turn survived into the next", row)
	}
}
