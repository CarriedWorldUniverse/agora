// Package conformance is the U18 end-to-end suite: it boots a real daemon and
// drives the golden flows through real clients. The suite is authored RED at
// U0 (agora-spec-build.md §0.7a): each test loads and sanity-checks its
// golden fixture, then skips with the unit that flips it live. A unit's DoD
// includes replacing its skip with the live drive IN THE SAME PR.
package conformance

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func loadFlow(t *testing.T, name string) []contracts.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "contracts", "testdata", "flows", name))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	defer f.Close()
	var evs []contracts.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var ev contracts.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("fixture line: %v", err)
		}
		evs = append(evs, ev)
	}
	if len(evs) == 0 {
		t.Fatal("empty flow")
	}
	return evs
}

// TestFlowTurn — pipe mode: user_message in, the turn.jsonl event sequence
// out (stub engine). Flips live at U2 (io).
func TestFlowTurn(t *testing.T) {
	_ = loadFlow(t, "turn.jsonl")
	t.Skip("pending U2: daemon + pipe runner")
}

// TestFlowApproval — exec approval fan-out, first-answer-wins, attributed
// resolution. Flips live at U7 (approvals engine) over U2.
func TestFlowApproval(t *testing.T) {
	_ = loadFlow(t, "approval.jsonl")
	t.Skip("pending U7: approvals engine (over U2)")
}

// TestFlowQuestionParkResume — blocking question parks the thread durably
// (daemon restart inside this test once live), answer resumes it. Flips live
// at U11 (planning+questions).
func TestFlowQuestionParkResume(t *testing.T) {
	_ = loadFlow(t, "question_park_resume.jsonl")
	t.Skip("pending U11: question ladder + waiting-on-answer state")
}

// TestFlowPlanGate — plan submit raises the gate; allow refused while
// open_questions remain; answered → allow → exit. Flips live at U11.
func TestFlowPlanGate(t *testing.T) {
	_ = loadFlow(t, "plan_gate.jsonl")
	t.Skip("pending U11: planning posture + plan gate")
}

// TestFlowResumeFork — thread replay, fork-by-reference, wd-filtered list.
// Flips live at U3 (persistence). Fixture is the store's own golden set,
// added with U3 in the same PR.
func TestFlowResumeFork(t *testing.T) {
	t.Skip("pending U3: persistence (fixture lands with the unit)")
}

// TestFlowCompactionCuration — compaction pair + curation demote/readmit
// events under forced pressure. Flips live at U12 (context).
func TestFlowCompactionCuration(t *testing.T) {
	t.Skip("pending U12: context manager + curation (fixture lands with the unit)")
}

// TestFlowPodProvision — blank pod boots, atomic provision, drive a turn,
// blocked:needs-input round-trip. Flips live at U17.
func TestFlowPodProvision(t *testing.T) {
	t.Skip("pending U17: pod mode + provision (fixture lands with the unit)")
}
