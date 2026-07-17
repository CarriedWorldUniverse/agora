package planning

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
)

func newMemThread(t *testing.T, threadID string) contracts.ThreadStore {
	t.Helper()
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{
		ThreadID:  threadID,
		CreatedAt: time.Unix(0, 0).UTC(),
		Profile:   "dev",
	}); err != nil {
		t.Fatalf("Create thread: %v", err)
	}
	return store
}

// newMemStoreWithThreads builds a single MemStore with n pre-created
// threads ("th_concurrent_0".."th_concurrent_{n-1}") — used by concurrency
// tests that need multiple independent threads on one shared store.
func newMemStoreWithThreads(t *testing.T, n int) contracts.ThreadStore {
	t.Helper()
	store := persistence.NewMemStore()
	for i := 0; i < n; i++ {
		threadID := fmt.Sprintf("th_concurrent_%d", i)
		if err := store.Create(contracts.ThreadMeta{
			ThreadID:  threadID,
			CreatedAt: time.Unix(0, 0).UTC(),
			Profile:   "dev",
		}); err != nil {
			t.Fatalf("Create thread %s: %v", threadID, err)
		}
	}
	return store
}

func TestGate_RefusesAllowWithOpenQuestions(t *testing.T) {
	req := GateRequest{
		Plan: contracts.PlanArtifact{
			Phase:  contracts.PhasePlan,
			Submit: true,
			OpenQuestions: []contracts.QuestionAsked{
				{ID: "q_1", Source: contracts.QuestionFromAgent, Blocking: true},
			},
		},
		Decision: contracts.DecisionAllow,
		Exit:     contracts.ExitInline,
		By:       "operator:jacinta",
	}

	out, err := Gate(req)
	if !errors.Is(err, ErrOpenQuestions) {
		t.Fatalf("Gate() error = %v; want ErrOpenQuestions", err)
	}
	if !out.Revise {
		t.Fatalf("Gate() outcome.Revise = false; want true (revision loop)")
	}
	if out.Resolution.Decision != contracts.DecisionDeny {
		t.Fatalf("Gate() resolution decision = %q; want deny (invariant 6 overrides operator allow)", out.Resolution.Decision)
	}
	if out.Exit != "" {
		t.Fatalf("Gate() exit = %q; want empty (gate refused)", out.Exit)
	}
}

func TestGate_AllowExitsCleanPlan(t *testing.T) {
	for _, exit := range []contracts.PlanExit{contracts.ExitInline, contracts.ExitDelegate} {
		t.Run(string(exit), func(t *testing.T) {
			out, err := Gate(GateRequest{
				Plan: contracts.PlanArtifact{
					Phase:  contracts.PhasePlan,
					Submit: true,
					Steps:  []string{"do the thing"},
				},
				Decision: contracts.DecisionAllow,
				Exit:     exit,
				By:       "operator:jacinta",
				Message:  "approved: go",
			})
			if err != nil {
				t.Fatalf("Gate() unexpected error: %v", err)
			}
			if out.Revise {
				t.Fatalf("Gate() outcome.Revise = true; want false on a clean allow")
			}
			if out.Resolution.Decision != contracts.DecisionAllow {
				t.Fatalf("Gate() resolution decision = %q; want allow", out.Resolution.Decision)
			}
			if out.Exit != exit {
				t.Fatalf("Gate() exit = %q; want %q", out.Exit, exit)
			}
			if out.Resolution.By != "operator:jacinta" {
				t.Fatalf("Gate() resolution.By = %q; want operator:jacinta", out.Resolution.By)
			}
		})
	}
}

func TestGate_DenyEntersRevisionLoop(t *testing.T) {
	out, err := Gate(GateRequest{
		Plan: contracts.PlanArtifact{
			Phase:  contracts.PhasePlan,
			Submit: true,
			Steps:  []string{"do the thing"},
		},
		Decision: contracts.DecisionDeny,
		By:       "operator:jacinta",
		Message:  "not detailed enough, revise the work_items",
	})
	if err != nil {
		t.Fatalf("Gate() unexpected error: %v", err)
	}
	if !out.Revise {
		t.Fatalf("Gate() outcome.Revise = false; want true on deny")
	}
	if out.Resolution.Decision != contracts.DecisionDeny {
		t.Fatalf("Gate() resolution decision = %q; want deny", out.Resolution.Decision)
	}
	if out.Resolution.Message != "not detailed enough, revise the work_items" {
		t.Fatalf("Gate() resolution.Message = %q; want the deny feedback preserved", out.Resolution.Message)
	}
	if out.Exit != "" {
		t.Fatalf("Gate() exit = %q; want empty on deny", out.Exit)
	}
}

func TestGate_RequiresSubmit(t *testing.T) {
	_, err := Gate(GateRequest{
		Plan:     contracts.PlanArtifact{Phase: contracts.PhasePlan, Steps: []string{"draft"}},
		Decision: contracts.DecisionAllow,
		Exit:     contracts.ExitInline,
	})
	if !errors.Is(err, ErrPlanNotSubmitted) {
		t.Fatalf("Gate() error = %v; want ErrPlanNotSubmitted", err)
	}
}

func TestGate_UnknownExitRejected(t *testing.T) {
	_, err := Gate(GateRequest{
		Plan:     contracts.PlanArtifact{Submit: true},
		Decision: contracts.DecisionAllow,
		Exit:     contracts.PlanExit("sideways"),
	})
	if !errors.Is(err, ErrUnknownExit) {
		t.Fatalf("Gate() error = %v; want ErrUnknownExit", err)
	}
}

func TestGate_UnknownDecisionFailsClosed(t *testing.T) {
	_, err := Gate(GateRequest{
		Plan:     contracts.PlanArtifact{Submit: true},
		Decision: contracts.Decision("maybe"),
	})
	if !errors.Is(err, ErrUnknownDecision) {
		t.Fatalf("Gate() error = %v; want ErrUnknownDecision", err)
	}
}

func TestPlanLog_AppendOnlyRevisions(t *testing.T) {
	store := newMemThread(t, "th_plan1")
	log := NewPlanLog(store)

	rev1 := contracts.PlanArtifact{Phase: contracts.PhaseDesign, Steps: []string{"sketch"}}
	rev2 := contracts.PlanArtifact{
		Phase:  contracts.PhasePlan,
		Steps:  []string{"sketch", "implement"},
		Submit: true,
	}

	if err := log.Update("th_plan1", rev1, time.Unix(1, 0).UTC(), "agent:builder"); err != nil {
		t.Fatalf("Update rev1: %v", err)
	}
	if err := log.Update("th_plan1", rev2, time.Unix(2, 0).UTC(), "agent:builder"); err != nil {
		t.Fatalf("Update rev2: %v", err)
	}

	got, found, err := log.Current("th_plan1")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if !found {
		t.Fatalf("Current found = false; want true")
	}
	if got.Phase != contracts.PhasePlan || len(got.Steps) != 2 || !got.Submit {
		t.Fatalf("Current() = %+v; want the latest revision (rev2)", got)
	}

	// Never rewritten: both revisions are still distinct items in the log.
	it, err := store.Resume("th_plan1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer it.Close()
	var revisions int
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		if item.Type == contracts.TIPlanRevision {
			revisions++
		}
	}
	if revisions != 2 {
		t.Fatalf("thread log has %d plan_revision items; want 2 (append-only, never rewritten)", revisions)
	}
}

func TestPlanLog_CurrentOnEmptyThread(t *testing.T) {
	store := newMemThread(t, "th_empty")
	log := NewPlanLog(store)

	_, found, err := log.Current("th_empty")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if found {
		t.Fatalf("Current found = true on a thread with no plan tool calls; want false")
	}
}
