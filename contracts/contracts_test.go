package contracts

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// knownEvents is the closed set a fixture line's type must belong to.
var knownEvents = map[EventType]bool{
	EvThreadStarted: true, EvTurnStarted: true, EvTurnCompleted: true, EvTurnFailed: true,
	EvItemStarted: true, EvItemUpdated: true, EvItemCompleted: true, EvAgentMessageDelta: true,
	EvToolLoaded:        true,
	EvCompactionStarted: true, EvCompactionCompleted: true,
	EvCurationDemoted: true, EvCurationReadmitted: true,
	EvApprovalRequested: true, EvApprovalResolved: true,
	EvQuestionAsked: true, EvQuestionAnswered: true,
	EvThreadWaiting: true, EvThreadResumed: true,
	EvClientAttached: true, EvClientDetached: true,
	EvError: true,
}

var knownItems = map[ItemType]bool{
	ItemAgentMessage: true, ItemReasoning: true, ItemCommandExecution: true,
	ItemFileChange: true, ItemMCPToolCall: true, ItemPlan: true,
	ItemAgentSpawn: true, ItemWorkflowProgress: true,
}

func fixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "flows", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		b := make([]byte, len(sc.Bytes()))
		copy(b, sc.Bytes())
		lines = append(lines, b)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("empty fixture")
	}
	return lines
}

func allFlows(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join("testdata", "flows", "*.jsonl"))
	if err != nil || len(m) == 0 {
		t.Fatalf("no flow fixtures found: %v", err)
	}
	names := make([]string, len(m))
	for i, p := range m {
		names[i] = filepath.Base(p)
	}
	return names
}

// TestFixturesDecodeAsEvents: every golden line decodes into the Event
// envelope with a known type; item refs use known item types; envelopes
// survive a marshal round-trip semantically (agora-spec-io §1).
func TestFixturesDecodeAsEvents(t *testing.T) {
	for _, name := range allFlows(t) {
		t.Run(name, func(t *testing.T) {
			for i, line := range fixtureLines(t, name) {
				var ev Event
				if err := json.Unmarshal(line, &ev); err != nil {
					t.Fatalf("line %d: %v", i+1, err)
				}
				if !knownEvents[ev.Type] {
					t.Fatalf("line %d: unknown event type %q", i+1, ev.Type)
				}
				if ev.Item != nil && !knownItems[ev.Item.Type] {
					t.Fatalf("line %d: unknown item type %q", i+1, ev.Item.Type)
				}
				// Round-trip: envelope fields must re-marshal losslessly.
				out, err := json.Marshal(ev)
				if err != nil {
					t.Fatalf("line %d: re-marshal: %v", i+1, err)
				}
				var ev2 Event
				if err := json.Unmarshal(out, &ev2); err != nil {
					t.Fatalf("line %d: re-decode: %v", i+1, err)
				}
				if ev2.Type != ev.Type || ev2.ThreadID != ev.ThreadID || ev2.TurnID != ev.TurnID {
					t.Fatalf("line %d: envelope not stable across round-trip", i+1)
				}
			}
		})
	}
}

// TestApprovalPayloadsTyped: approval.requested payloads decode into
// ApprovalRequest with a canonical kind; resolutions carry attribution
// (approvals §1, §4 invariant 3).
func TestApprovalPayloadsTyped(t *testing.T) {
	knownKinds := map[ApprovalKind]bool{
		KindExec: true, KindPatch: true, KindEscalation: true,
		KindMCPTool: true, KindQuestion: true, KindPlan: true, KindGate: true,
	}
	for _, name := range allFlows(t) {
		for i, line := range fixtureLines(t, name) {
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				t.Fatal(err)
			}
			switch ev.Type {
			case EvApprovalRequested:
				var ar ApprovalRequest
				if err := json.Unmarshal(ev.Payload, &ar); err != nil {
					t.Fatalf("%s line %d: %v", name, i+1, err)
				}
				if ar.ID == "" || !knownKinds[ar.Kind] {
					t.Fatalf("%s line %d: bad ApprovalRequest %+v", name, i+1, ar)
				}
			case EvApprovalResolved:
				var res ApprovalResolution
				if err := json.Unmarshal(ev.Payload, &res); err != nil {
					t.Fatalf("%s line %d: %v", name, i+1, err)
				}
				if res.By == "" || res.Stage == "" {
					t.Fatalf("%s line %d: resolution lacks attribution: %+v", name, i+1, res)
				}
			}
		}
	}
}

// TestQuestionFlowShape: the park/resume fixture honors the ladder — a
// blocking question parks the thread, an attributed answer resumes it, and
// nothing between waiting and answered advances the turn
// (planning-questions §5–§6).
func TestQuestionFlowShape(t *testing.T) {
	lines := fixtureLines(t, "question_park_resume.jsonl")
	var seenAsked, seenWaiting, seenAnswered, seenResumed bool
	for i, line := range fixtureLines(t, "question_park_resume.jsonl") {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatal(err)
		}
		switch ev.Type {
		case EvQuestionAsked:
			seenAsked = true
			var q struct {
				ID       string          `json:"id"`
				Blocking bool            `json:"blocking"`
				Payload  QuestionPayload `json:"payload"`
			}
			if err := json.Unmarshal(ev.Payload, &q); err != nil {
				t.Fatal(err)
			}
			if !q.Blocking || q.Payload.Text == "" || q.Payload.Source != QuestionFromAgent {
				t.Fatalf("line %d: malformed question %+v", i+1, q)
			}
		case EvThreadWaiting:
			if !seenAsked {
				t.Fatal("thread.waiting before question.asked")
			}
			seenWaiting = true
		case EvQuestionAnswered:
			if !seenWaiting {
				t.Fatal("answer before park")
			}
			var a struct {
				ID     string `json:"id"`
				Answer Answer `json:"answer"`
			}
			if err := json.Unmarshal(ev.Payload, &a); err != nil {
				t.Fatal(err)
			}
			if a.Answer.By == "" {
				t.Fatal("answer without attribution — never-fabricate requires an actor")
			}
			seenAnswered = true
		case EvThreadResumed:
			if !seenAnswered {
				t.Fatal("resume before answer")
			}
			seenResumed = true
		case EvTurnStarted:
			if seenWaiting && !seenResumed && ev.TurnID != "tu_0001" {
				t.Fatal("new turn started while parked")
			}
		}
	}
	if !(seenAsked && seenWaiting && seenAnswered && seenResumed) {
		t.Fatalf("incomplete park/resume flow across %d lines", len(lines))
	}
}

// TestPlanGateShape: the plan-gate fixture honors the open-questions
// invariant — the gate resolves allow only AFTER the open question is
// answered (planning-questions §3, invariant 6).
func TestPlanGateShape(t *testing.T) {
	var answered bool
	for i, line := range fixtureLines(t, "plan_gate.jsonl") {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatal(err)
		}
		switch ev.Type {
		case EvApprovalRequested:
			var ar ApprovalRequest
			if err := json.Unmarshal(ev.Payload, &ar); err != nil {
				t.Fatal(err)
			}
			if ar.Kind != KindPlan {
				continue
			}
			raw, _ := json.Marshal(ar.Payload)
			var pa PlanArtifact
			if err := json.Unmarshal(raw, &pa); err != nil {
				t.Fatalf("line %d: plan payload: %v", i+1, err)
			}
			if !pa.Submit {
				t.Fatalf("line %d: gate raised without submit", i+1)
			}
		case EvQuestionAnswered:
			answered = true
		case EvApprovalResolved:
			var res ApprovalResolution
			if err := json.Unmarshal(ev.Payload, &res); err != nil {
				t.Fatal(err)
			}
			if res.Decision == DecisionAllow && !answered {
				t.Fatalf("line %d: plan gate allowed with unresolved open question", i+1)
			}
		}
	}
	if !answered {
		t.Fatal("fixture never answered the open question")
	}
}

// TestTurnCompletedCarriesUsage: usage is required on turn.completed
// (bridle §2: usage is required; io §1).
func TestTurnCompletedCarriesUsage(t *testing.T) {
	for _, name := range allFlows(t) {
		for i, line := range fixtureLines(t, name) {
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Type != EvTurnCompleted {
				continue
			}
			var p struct {
				Usage *Usage `json:"usage"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Usage == nil {
				t.Fatalf("%s line %d: turn.completed without usage", name, i+1)
			}
		}
	}
}
