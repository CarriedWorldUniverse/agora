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
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
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

// resumeForkOp is one line of resume_fork.jsonl — a scripted ThreadStore
// operation (this flow drives a store, not the io event stream, so its
// fixture shape is its own — see conformance/testdata/flows/resume_fork.jsonl).
type resumeForkOp struct {
	Op        string `json:"op"`
	ThreadID  string `json:"thread_id,omitempty"`
	ThreadRef string `json:"thread_ref,omitempty"`

	// create
	IdentityFP string `json:"identity_fp,omitempty"`
	Profile    string `json:"profile,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`

	// append
	Items []struct {
		Type    contracts.ThreadItemType `json:"type"`
		Payload any                      `json:"payload"`
	} `json:"items,omitempty"`

	// fork
	Seq       int64  `json:"seq,omitempty"`
	ResultRef string `json:"result_ref,omitempty"`

	// resume assertions
	ExpectPayloads []string `json:"expect_payloads,omitempty"`

	// list
	Filter struct {
		WorkingDir string `json:"working_dir,omitempty"`
	} `json:"filter,omitempty"`
	ExpectThreadIDs []string `json:"expect_thread_ids,omitempty"`
}

// loadResumeForkOps reads its own fixture from conformance/testdata/flows
// (NOT contracts/testdata/flows — that dir's fixtures are walked by
// contracts_test.go's TestFixturesDecodeAsEvents and must decode as the
// io-event Event envelope; this flow drives a ThreadStore, a different
// shape, so it gets its own testdata directory rather than breaking that
// invariant or teaching the shared loader two fixture shapes).
func loadResumeForkOps(t *testing.T, name string) []resumeForkOp {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "flows", name))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	defer f.Close()
	var ops []resumeForkOp
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var op resumeForkOp
		if err := json.Unmarshal(sc.Bytes(), &op); err != nil {
			t.Fatalf("fixture line: %v", err)
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		t.Fatal("empty flow")
	}
	return ops
}

// TestFlowResumeFork — thread replay, fork-by-reference, wd-filtered list.
// Flips live at U3 (persistence): drives a real persistence.LocalStore, in a
// temp dir, through the store's own golden op script
// (resume_fork.jsonl) — create/append/fork/resume/list, asserting the
// fork-by-reference chain-through + post-fork isolation invariant and the
// working_dir list filter.
func TestFlowResumeFork(t *testing.T) {
	ops := loadResumeForkOps(t, "resume_fork.jsonl")

	store, err := persistence.NewLocalStore(t.TempDir(), persistence.Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// thread_ref lets the fixture name a fork's result without knowing the
	// store-minted id ahead of time.
	refs := map[string]string{}
	resolve := func(id, ref string) string {
		if ref != "" {
			if resolved, ok := refs[ref]; ok {
				return resolved
			}
			t.Fatalf("unresolved thread_ref %q", ref)
		}
		return id
	}

	for i, op := range ops {
		switch op.Op {
		case "create":
			meta := contracts.ThreadMeta{
				ThreadID:   op.ThreadID,
				CreatedAt:  time.Now().UTC(),
				IdentityFP: op.IdentityFP,
				Profile:    op.Profile,
				WorkingDir: op.WorkingDir,
			}
			if err := store.Create(meta); err != nil {
				t.Fatalf("op %d create: %v", i, err)
			}

		case "append":
			threadID := resolve(op.ThreadID, op.ThreadRef)
			items := make([]contracts.ThreadItem, len(op.Items))
			for j, it := range op.Items {
				items[j] = contracts.ThreadItem{Type: it.Type, Payload: it.Payload, TS: time.Now().UTC()}
			}
			if err := store.Append(threadID, items); err != nil {
				t.Fatalf("op %d append: %v", i, err)
			}

		case "fork":
			threadID := resolve(op.ThreadID, op.ThreadRef)
			child, err := store.Fork(threadID, op.Seq)
			if err != nil {
				t.Fatalf("op %d fork: %v", i, err)
			}
			if op.ResultRef != "" {
				refs[op.ResultRef] = child.ThreadID
			}

		case "resume":
			threadID := resolve(op.ThreadID, op.ThreadRef)
			it, err := store.Resume(threadID)
			if err != nil {
				t.Fatalf("op %d resume: %v", i, err)
			}
			var got []string
			for {
				item, ok := it.Next()
				if !ok {
					break
				}
				payload, _ := item.Payload.(string)
				got = append(got, payload)
			}
			_ = it.Close()
			if !stringSlicesEqual(got, op.ExpectPayloads) {
				t.Fatalf("op %d resume(%s): got %v, want %v", i, threadID, got, op.ExpectPayloads)
			}

		case "list":
			metas, err := store.List(contracts.ListFilter{WorkingDir: op.Filter.WorkingDir})
			if err != nil {
				t.Fatalf("op %d list: %v", i, err)
			}
			gotIDs := make([]string, 0, len(metas))
			for _, m := range metas {
				gotIDs = append(gotIDs, m.ThreadID)
			}
			wantIDs := make([]string, len(op.ExpectThreadIDs))
			for j, w := range op.ExpectThreadIDs {
				wantIDs[j] = w
				if resolved, ok := refs[w]; ok {
					wantIDs[j] = resolved
				}
			}
			if !stringSetsEqual(gotIDs, wantIDs) {
				t.Fatalf("op %d list: got %v, want %v", i, gotIDs, wantIDs)
			}

		default:
			t.Fatalf("op %d: unknown op %q", i, op.Op)
		}
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]int{}
	for _, s := range a {
		set[s]++
	}
	for _, s := range b {
		set[s]--
	}
	for _, v := range set {
		if v != 0 {
			return false
		}
	}
	return true
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
