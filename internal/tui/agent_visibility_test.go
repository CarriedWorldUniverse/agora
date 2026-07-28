package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// These tests pin the property the agora#152 incident exposed: while a
// foreground agent() child runs, the operator must be able to SEE it. The
// entire visible state during that incident was
// "⣾ running · <model> · <elapsed>" for 30+ minutes, which is
// indistinguishable from a slow turn — so it read as a frozen session rather
// than something to interrupt.

func agentSpawnEvent(t *testing.T, evType contracts.EventType, seq int64, agentType, errMsg string) contracts.Event {
	t.Helper()
	body := map[string]any{}
	if agentType != "" {
		body["agent_type"] = agentType
	}
	if errMsg != "" {
		body["error"] = errMsg
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return contracts.Event{
		Type:    evType,
		Item:    &contracts.ItemRef{Seq: seq, Type: contracts.ItemAgentSpawn},
		Payload: payload,
	}
}

func visModel(t *testing.T, printed *[]string, now *time.Time) *Model {
	t.Helper()
	return NewModel(Config{
		AgentID: "agora",
		Theme:   PlainTheme(),
		Printer: capturingPrinter(printed),
		Now:     func() time.Time { return *now },
	})
}

func TestAgentSpawn_ShowsInStatusRowWhileRunning(t *testing.T) {
	var printed []string
	now := time.Unix(0, 0).UTC()
	m := visModel(t, &printed, &now)
	m.running = true
	m.turnStart = now

	if seg := m.agentsSegment(); seg != "" {
		t.Errorf("agentsSegment with nothing running = %q; want empty so the common case is unchanged", seg)
	}

	m.handleEvent(agentSpawnEvent(t, contracts.EvItemStarted, 1, "reviewer", ""))
	if len(printed) == 0 || !strings.Contains(strings.Join(printed, "\n"), "agent(reviewer) started") {
		t.Errorf("no spawn line printed; got %v", printed)
	}

	// Four minutes later, still running: the status row must say so.
	now = now.Add(4 * time.Minute)
	seg := m.agentsSegment()
	if !strings.Contains(seg, "reviewer") || !strings.Contains(seg, "4m0s") {
		t.Errorf("agentsSegment = %q; want the agent type and its elapsed time — this is the signal that was missing for 30+ minutes (agora#155)", seg)
	}
	if !strings.Contains(m.renderStatusRow(), "reviewer") {
		t.Errorf("status row = %q; want the running child visible without scrolling", m.renderStatusRow())
	}
}

func TestAgentSpawn_ClearsOnCompletion(t *testing.T) {
	var printed []string
	now := time.Unix(0, 0).UTC()
	m := visModel(t, &printed, &now)
	m.running = true
	m.turnStart = now

	m.handleEvent(agentSpawnEvent(t, contracts.EvItemStarted, 7, "explore", ""))
	now = now.Add(90 * time.Second)
	m.handleEvent(agentSpawnEvent(t, contracts.EvItemCompleted, 7, "", ""))

	if seg := m.agentsSegment(); seg != "" {
		t.Errorf("agentsSegment after completion = %q; want empty", seg)
	}
	out := strings.Join(printed, "\n")
	if !strings.Contains(out, "agent(explore) finished in 1m30s") {
		t.Errorf("no finish line with duration; got:\n%s", out)
	}
}

func TestAgentSpawn_ErrorIsReported(t *testing.T) {
	var printed []string
	now := time.Unix(0, 0).UTC()
	m := visModel(t, &printed, &now)
	m.running = true

	m.handleEvent(agentSpawnEvent(t, contracts.EvItemStarted, 3, "builder", ""))
	m.handleEvent(agentSpawnEvent(t, contracts.EvItemCompleted, 3, "", "spawn cap reached"))

	out := strings.Join(printed, "\n")
	if !strings.Contains(out, "spawn cap reached") {
		t.Errorf("agent failure not surfaced; got:\n%s", out)
	}
	if seg := m.agentsSegment(); seg != "" {
		t.Errorf("a failed agent stayed in the running set: %q", seg)
	}
}

func TestAgentSpawn_MultipleShowsCountAndOldest(t *testing.T) {
	var printed []string
	now := time.Unix(0, 0).UTC()
	m := visModel(t, &printed, &now)
	m.running = true

	m.handleEvent(agentSpawnEvent(t, contracts.EvItemStarted, 1, "reviewer", ""))
	now = now.Add(5 * time.Minute)
	m.handleEvent(agentSpawnEvent(t, contracts.EvItemStarted, 2, "explore", ""))
	now = now.Add(1 * time.Minute)

	seg := m.agentsSegment()
	if !strings.Contains(seg, "2 agents") {
		t.Errorf("agentsSegment = %q; want a count for concurrent children", seg)
	}
	// The oldest child is the one worth noticing — 6m, not the 1m one.
	if !strings.Contains(seg, "6m0s") {
		t.Errorf("agentsSegment = %q; want the OLDEST child's age (6m0s), which is the one worth noticing", seg)
	}
}
