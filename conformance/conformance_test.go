// Package conformance is the U18 end-to-end suite: it boots a real daemon and
// drives the golden flows through real clients. The suite is authored RED at
// U0 (agora-spec-build.md §0.7a): each test loads and sanity-checks its
// golden fixture, then skips with the unit that flips it live. A unit's DoD
// includes replacing its skip with the live drive IN THE SAME PR.
package conformance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
)

// rawFlow reads a contracts/testdata/flows fixture's raw bytes (for a
// byte-for-byte golden comparison, as opposed to loadFlow's decoded form).
func rawFlow(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "contracts", "testdata", "flows", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

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
// out (stub engine). Pure item/turn wire mechanics (blueprint §3.1); reuses
// agoraio.RunPipe + agoraio.ScriptedEngine per blueprint's explicit "REUSE"
// call — there is no approval/planning/ctxmgr seam in this flow to prove
// against a real implementation, unlike every other flow in this file.
func TestFlowTurn(t *testing.T) {
	events := loadFlow(t, "turn.jsonl")
	got := driveFlowTurn(t, []agoraio.ScriptedTurn{{Events: events}})
	want := rawFlow(t, "turn.jsonl")
	if !bytes.Equal(got, want) {
		t.Fatalf("stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestFlowApproval — exec approval fan-out, first-answer-wins, attributed
// resolution. Two sub-drives (blueprint §3.2): 3.2a pipe mode, byte-for-byte
// against the golden fixture, attribution via a fixed pipe-mode constant
// (no arbitration to resolve — one implicit client); 3.2b session protocol,
// TWO real clients racing a genuine first-answer-wins resolution over a real
// daemon + unix socket, plus the U16 third-device attach-refusal handoff
// (TestFlowApproval_SessionProtocolFanOut, its own top-level test — Go test
// output needs it addressable/skippable on its own, but it is the second
// half of this flow's DoD).
func TestFlowApproval(t *testing.T) {
	t.Run("pipe", func(t *testing.T) {
		got := driveFlowApprovalPipe(t)
		want := rawFlow(t, "approval.jsonl")
		if !bytes.Equal(got, want) {
			t.Fatalf("stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})
	t.Run("session_protocol_fanout", sessionProtocolFanOut)
}

// TestFlowQuestionParkResume — blocking question parks the thread durably
// (a real daemon restart inside this test), answer resumes it. Live drive in
// flow_question_park_resume_test.go (blueprint §3.3).

// TestFlowPlanGate — plan submit raises the gate; allow refused while
// open_questions remain; answered → allow → exit. Live drive in
// flow_plan_gate_test.go (blueprint §3.4).

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
// events under forced pressure. Live drive in
// flow_compaction_curation_test.go (blueprint §3.5).

// TestFlowPodProvision — blank pod boots, atomic provision, drive a turn,
// blocked:needs-input round-trip. Live drive in flow_pod_provision_test.go
// (blueprint §3.6).
