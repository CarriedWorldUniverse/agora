package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func uc5bModel(printed *[]string) *Model {
	return NewModel(Config{
		Backend: newFakeBackend(),
		AgentID: "agora",
		Theme:   PlainTheme(),
		Printer: capturingPrinter(printed),
		Now:     func() time.Time { return time.Unix(0, 0).UTC() },
	})
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// TestHandleEvent_ToolItems_RenderAndSeparate: a tool call between two prose
// segments (neither newline-terminated) must (a) render a tool line and (b)
// FLUSH the first segment so it no longer fuses with the second ("code.All").
func TestHandleEvent_ToolItems_RenderAndSeparate(t *testing.T) {
	var printed []string
	m := uc5bModel(&printed)

	m.handleEvent(contracts.Event{Type: contracts.EvTurnStarted, TurnID: "t"})
	m.handleEvent(contracts.Event{Type: contracts.EvAgentMessageDelta, Payload: raw(`{"text":"checking the code."}`)})
	m.handleEvent(contracts.Event{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Type: contracts.ItemCommandExecution}, Payload: raw(`{"command":"go build ./..."}`)})
	m.handleEvent(contracts.Event{Type: contracts.EvItemCompleted, Item: &contracts.ItemRef{Type: contracts.ItemCommandExecution}, Payload: raw(`{"error":""}`)})
	m.handleEvent(contracts.Event{Type: contracts.EvAgentMessageDelta, Payload: raw(`{"text":"All good."}`)})
	m.handleEvent(contracts.Event{Type: contracts.EvTurnCompleted, TurnID: "t"})

	joined := strings.Join(printed, "\n")
	if !strings.Contains(joined, "$ go build ./...") {
		t.Fatalf("tool line not rendered; printed=%q", printed)
	}
	// The two prose segments must NOT be fused into one printed entry.
	for _, p := range printed {
		if strings.Contains(p, "code.") && strings.Contains(p, "All good") {
			t.Fatalf("segments fused in one line %q; printed=%q", p, printed)
		}
	}
	if !strings.Contains(joined, "checking the code.") || !strings.Contains(joined, "All good.") {
		t.Fatalf("both prose segments should be present separately; printed=%q", printed)
	}
}

// TestHandleEvent_ToolItems_ErrorSurfaces: a failed tool prints a danger line;
// a successful one prints nothing on completion.
func TestHandleEvent_ToolItems_ErrorSurfaces(t *testing.T) {
	var printed []string
	m := uc5bModel(&printed)
	m.handleEvent(contracts.Event{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Type: contracts.ItemFileChange}, Payload: raw(`{"path":"x.go"}`)})
	m.handleEvent(contracts.Event{Type: contracts.EvItemCompleted, Item: &contracts.ItemRef{Type: contracts.ItemFileChange}, Payload: raw(`{"path":"x.go","error":"permission denied"}`)})
	joined := strings.Join(printed, "\n")
	if !strings.Contains(joined, "edit x.go") {
		t.Fatalf("file-change start not rendered; printed=%q", printed)
	}
	if !strings.Contains(joined, "permission denied") {
		t.Fatalf("tool error not surfaced; printed=%q", printed)
	}
}

// TestHandleEvent_NonToolItems_Ignored: agent_message / reasoning item events
// (and a nil Item) must NOT emit a tool line.
func TestHandleEvent_NonToolItems_Ignored(t *testing.T) {
	var printed []string
	m := uc5bModel(&printed)
	m.handleEvent(contracts.Event{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Type: contracts.ItemAgentMessage}, Payload: raw(`{"text":"hi"}`)})
	m.handleEvent(contracts.Event{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Type: contracts.ItemReasoning}, Payload: raw(`{"text":"think"}`)})
	m.handleEvent(contracts.Event{Type: contracts.EvItemStarted, Item: nil})
	m.handleEvent(contracts.Event{Type: contracts.EvItemCompleted, Item: &contracts.ItemRef{Type: contracts.ItemAgentMessage}, Payload: raw(`{}`)})
	if len(printed) != 0 {
		t.Fatalf("non-tool item events emitted output: %q", printed)
	}
}
