package turnengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// The end-to-end that matters for the durable-permissions unit: an operator
// approving with a wider-than-once scope during a REAL turn must reach the
// file, and a fresh session over the same file must honour it without
// asking again.
//
// This exists because the failure mode it guards against has already
// happened once in this codebase: subagent.NewRegistry(nil) meant a whole
// tested subsystem was never reachable in production (#100). A store that
// works perfectly in its own unit tests but is not actually consulted by
// the Manager would be the identical bug.
func TestManager_FileScopeStore_GrantPersistsAcrossSessions(t *testing.T) {
	roots := managerTestRoots(t)
	path := filepath.Join(t.TempDir(), "permissions.json")
	project := roots.WorkingDir

	store1, warn := approval.OpenFileScopeStore(path, project)
	if warn != nil {
		t.Fatalf("open: %v", warn)
	}

	// Session 1: two writes; approve the first with session scope.
	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			writeFileCall("1", "one.txt", "first"),
			writeFileCall("2", "two.txt", "second"),
		}},
		fake.Step{Text: "done"},
	)
	m := NewManager("th_persist", provider, WithRoots(roots),
		WithPolicy(promptAllPolicy()), WithScopeStore(store1),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "write both files"}
	req := recvApprovalRequested(t, out, testTimeout)
	var ar contracts.ApprovalRequest
	if err := json.Unmarshal(req.Payload, &ar); err != nil {
		t.Fatalf("decode approval payload: %v", err)
	}
	in <- contracts.Input{Type: contracts.InApprovalResponse, ID: ar.ID,
		Decision: contracts.DecisionAllow, Scope: contracts.ScopeSession}
	if got := drainNoApprovalRequestedToTurnEnd(t, out, testTimeout); got != contracts.EvTurnCompleted {
		t.Fatalf("turn ended as %s; want turn.completed", got)
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The grant must have reached DISK, not just the in-memory store.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the Manager's approval never reached the permissions file: %v", err)
	}
	if !strings.Contains(string(data), "th_persist") {
		t.Fatalf("permissions file does not record the session grant:\n%s", data)
	}

	// Session 2: a brand-new store over the same file must already know.
	store2, warn := approval.OpenFileScopeStore(path, project)
	if warn != nil {
		t.Fatalf("reopen: %v", warn)
	}
	if _, ok := store2.Match(contracts.KindPatch, "th_persist", ""); !ok {
		t.Fatal("a new session did not inherit the saved grant — the store is wired but not durable end-to-end")
	}
}
