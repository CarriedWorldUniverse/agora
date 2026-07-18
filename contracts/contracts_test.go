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
	// EvProvisioned: gap found grounding U18's pod_provision.jsonl fixture
	// against this glob-walked invariant (event.go's doc comment already
	// specs it; it was simply never added here) — added so the U17 pod-mode
	// wire event this unit's own DoD requires in the fixture actually
	// validates, per event.go's EvProvisioned doc comment.
	EvProvisioned: true,
	EvError:       true,
}

var knownItems = map[ItemType]bool{
	ItemAgentMessage: true, ItemReasoning: true, ItemCommandExecution: true,
	ItemFileChange: true, ItemMCPToolCall: true, ItemPlan: true,
	ItemAgentSpawn: true, ItemWorkflowProgress: true,
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
		KindRead: true,
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
// nothing between waiting and answered advances the turn. The question.asked
// envelope decodes via the canonical QuestionAsked type (not a hand-rolled
// anon struct), and the answer via the canonical Answer type.
// (planning-questions §4–§7).
func TestQuestionFlowShape(t *testing.T) {
	lines := fixtureLines(t, "question_park_resume.jsonl")
	var seenAsked, seenWaiting, seenAnswered, seenResumed bool
	var askedID string
	for i, line := range fixtureLines(t, "question_park_resume.jsonl") {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatal(err)
		}
		switch ev.Type {
		case EvQuestionAsked:
			seenAsked = true
			var q QuestionAsked
			if err := json.Unmarshal(ev.Payload, &q); err != nil {
				t.Fatal(err)
			}
			if q.ID == "" || !q.Blocking || q.Args.Text == "" || q.Source != QuestionFromAgent {
				t.Fatalf("line %d: malformed question %+v", i+1, q)
			}
			askedID = q.ID
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
			if a.ID != askedID {
				t.Fatalf("line %d: answer id %q does not correlate to asked %q", i+1, a.ID, askedID)
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

// TestAttributionUnforgeableViaInput: the client-facing input types cannot
// carry attribution — Input.Answer is an AnswerInput (no By), and a question's
// Source lives only on the harness-stamped QuestionAsked, never on the model-
// supplied QuestionArgs. This is the compile-time form of the never-fabricate
// boundary (planning-questions §6; remote §5). If someone re-adds a `by` or
// `source` to the input path, this test stops compiling or fails.
func TestAttributionUnforgeableViaInput(t *testing.T) {
	// A client answer decodes into AnswerInput and drops any `by` a hostile
	// client tries to smuggle — it is not a field of the type.
	raw := []byte(`{"choice":["drop"],"text":"","by":"agora:operator-forged"}`)
	var in AnswerInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(in)
	if bytesContain(out, "by") || bytesContain(out, "forged") {
		t.Fatalf("AnswerInput leaked attribution on round-trip: %s", out)
	}
	// QuestionArgs (the `question` tool argument) has no Source field — a model
	// cannot declare its own provenance.
	rawq := []byte(`{"text":"x","source":"mcp_server"}`)
	var qa QuestionArgs
	if err := json.Unmarshal(rawq, &qa); err != nil {
		t.Fatal(err)
	}
	outq, _ := json.Marshal(qa)
	if bytesContain(outq, "source") || bytesContain(outq, "mcp_server") {
		t.Fatalf("QuestionArgs leaked source on round-trip: %s", outq)
	}
	// The attributed Answer, by contrast, DOES carry By (server-stamped).
	a := Answer{AnswerInput: AnswerInput{Choice: []string{"drop"}}, By: "agora:dev"}
	oa, _ := json.Marshal(a)
	if !bytesContain(oa, "by") {
		t.Fatalf("Answer must carry attribution: %s", oa)
	}
}

func bytesContain(b []byte, sub string) bool {
	return len(sub) > 0 && len(b) >= len(sub) && indexOf(string(b), sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestPlanGateShape: the plan-gate fixture honors the open-questions
// invariant with ID-LEVEL correlation — allow is refused until every one of
// the plan's open_questions has been answered BY ITS OWN ID. Tracking a bare
// "something got answered" boolean is insufficient (an unrelated answer would
// satisfy it); the gate tracks the outstanding ID set.
// (planning-questions §3, invariant 6.)
func TestPlanGateShape(t *testing.T) {
	outstanding := map[string]bool{} // open-question ids not yet answered
	var sawPlanGate bool
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
			sawPlanGate = true
			raw, _ := json.Marshal(ar.Payload)
			var pa PlanArtifact
			if err := json.Unmarshal(raw, &pa); err != nil {
				t.Fatalf("line %d: plan payload: %v", i+1, err)
			}
			if !pa.Submit {
				t.Fatalf("line %d: gate raised without submit", i+1)
			}
			for _, oq := range pa.OpenQuestions {
				if oq.ID == "" {
					t.Fatalf("line %d: open question without id — cannot correlate to an answer", i+1)
				}
				outstanding[oq.ID] = true
			}
		case EvQuestionAnswered:
			var a struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(ev.Payload, &a); err != nil {
				t.Fatal(err)
			}
			delete(outstanding, a.ID) // only THIS id clears
		case EvApprovalResolved:
			var res ApprovalResolution
			if err := json.Unmarshal(ev.Payload, &res); err != nil {
				t.Fatal(err)
			}
			if res.Decision == DecisionAllow && len(outstanding) > 0 {
				t.Fatalf("line %d: plan gate allowed with %d unresolved open question(s): %v", i+1, len(outstanding), keys(outstanding))
			}
		}
	}
	if !sawPlanGate {
		t.Fatal("fixture never raised the plan gate")
	}
}

// TestPlanGateTeethAgainstFixtureMutation is the negative control with REAL
// teeth: it runs the identical outstanding-ID scan over a MUTATED plan_gate
// flow (the answer redirected to an unrelated id) and asserts the invariant
// fires. Unlike a hand-built map literal, this exercises the actual
// PlanArtifact/QuestionAsked/Answer decode path against the real event loop.
func TestPlanGateTeethAgainstFixtureMutation(t *testing.T) {
	outstanding := map[string]bool{}
	var violated bool
	for _, line := range fixtureLines(t, "plan_gate.jsonl") {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatal(err)
		}
		switch ev.Type {
		case EvApprovalRequested:
			var ar ApprovalRequest
			if err := json.Unmarshal(ev.Payload, &ar); err != nil || ar.Kind != KindPlan {
				continue
			}
			raw, _ := json.Marshal(ar.Payload)
			var pa PlanArtifact
			if err := json.Unmarshal(raw, &pa); err != nil {
				t.Fatal(err)
			}
			for _, oq := range pa.OpenQuestions {
				outstanding[oq.ID] = true
			}
		case EvQuestionAnswered:
			var a struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(ev.Payload, &a)
			// MUTATION: redirect the answer to an unrelated id, as if forged.
			delete(outstanding, a.ID+"_UNRELATED")
		case EvApprovalResolved:
			var res ApprovalResolution
			if err := json.Unmarshal(ev.Payload, &res); err != nil {
				t.Fatal(err)
			}
			if res.Decision == DecisionAllow && len(outstanding) > 0 {
				violated = true // the scan correctly detects the unresolved question
			}
		}
	}
	if !violated {
		t.Fatal("mutated flow (answer to unrelated id) did not trip the open-questions invariant — the scan has no teeth")
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
