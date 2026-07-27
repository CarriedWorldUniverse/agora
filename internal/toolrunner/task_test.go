package toolrunner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func writeTasks(t *testing.T, f *TaskFamily, tasks []Task) Result {
	t.Helper()
	args, err := json.Marshal(map[string]any{"tasks": tasks})
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.Execute(context.Background(), Call{Name: contracts.ToolTaskWrite, Args: args})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

func TestTaskWrite_RendersChecklistWithProgress(t *testing.T) {
	f := NewTaskFamily()
	res := writeTasks(t, f, []Task{
		{Content: "read the spec", Status: TaskCompleted},
		{Content: "write the code", Status: TaskInProgress},
		{Content: "add tests", Status: TaskPending},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{"[x] read the spec", "[>] write the code", "[ ] add tests", "1/3 done"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("output missing %q; got:\n%s", want, res.Content)
		}
	}
}

// Replace-whole-list semantics: a second write is the new truth, not a
// merge. Anything else needs per-item ids and invites partial-update bugs.
func TestTaskWrite_ReplacesTheWholeList(t *testing.T) {
	f := NewTaskFamily()
	writeTasks(t, f, []Task{
		{Content: "first", Status: TaskPending},
		{Content: "second", Status: TaskPending},
	})
	res := writeTasks(t, f, []Task{{Content: "only this", Status: TaskPending}})

	if strings.Contains(res.Content, "first") || strings.Contains(res.Content, "second") {
		t.Fatalf("old tasks survived a replacing write; got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "0/1 done") {
		t.Fatalf("count not recomputed for the new list; got:\n%s", res.Content)
	}
}

// The load-bearing rule: one thing at a time.
func TestTaskWrite_RejectsMultipleInProgress(t *testing.T) {
	f := NewTaskFamily()
	res := writeTasks(t, f, []Task{
		{Content: "a", Status: TaskInProgress},
		{Content: "b", Status: TaskInProgress},
	})
	if !res.IsError {
		t.Fatal("two in_progress tasks were accepted; exactly one may be")
	}
	// The message must tell the model how to fix it, not just that it is wrong.
	if !strings.Contains(res.Content, "pending") {
		t.Errorf("error does not say how to correct it; got: %s", res.Content)
	}
	// A rejected write must not have mutated the list.
	if got := f.Tasks(); len(got) != 0 {
		t.Fatalf("rejected write still mutated state: %+v", got)
	}
}

func TestTaskWrite_OneInProgressIsFine(t *testing.T) {
	f := NewTaskFamily()
	res := writeTasks(t, f, []Task{
		{Content: "a", Status: TaskInProgress},
		{Content: "b", Status: TaskPending},
	})
	if res.IsError {
		t.Fatalf("a single in_progress task was rejected: %s", res.Content)
	}
}

func TestTaskWrite_RejectsBadStatusAndEmptyContent(t *testing.T) {
	f := NewTaskFamily()

	res := writeTasks(t, f, []Task{{Content: "a", Status: "blocked"}})
	if !res.IsError {
		t.Error("an unknown status was accepted")
	}
	if !strings.Contains(res.Content, "blocked") {
		t.Errorf("error does not name the offending status; got: %s", res.Content)
	}

	res = writeTasks(t, f, []Task{{Content: "   ", Status: TaskPending}})
	if !res.IsError {
		t.Error("whitespace-only content was accepted")
	}
}

// Clearing the list is legitimate — the work finished.
func TestTaskWrite_EmptyListIsAllowed(t *testing.T) {
	f := NewTaskFamily()
	writeTasks(t, f, []Task{{Content: "a", Status: TaskPending}})
	res := writeTasks(t, f, []Task{})
	if res.IsError {
		t.Fatalf("clearing the list was rejected: %s", res.Content)
	}
	if !strings.Contains(res.Content, "no tasks") {
		t.Fatalf("cleared list does not render as empty; got:\n%s", res.Content)
	}
}

// task.read exists for after a long stretch when the earlier write has
// fallen out of context.
func TestTaskRead_ReturnsCurrentList(t *testing.T) {
	f := NewTaskFamily()
	writeTasks(t, f, []Task{{Content: "still to do", Status: TaskPending}})

	res, err := f.Execute(context.Background(), Call{Name: contracts.ToolTaskRead, Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "still to do") {
		t.Fatalf("task.read did not return the list; got: %+v", res)
	}
}

func TestTaskRead_EmptyIsNotAnError(t *testing.T) {
	res, err := NewTaskFamily().Execute(context.Background(),
		Call{Name: contracts.ToolTaskRead, Args: json.RawMessage(`{}`)})
	if err != nil || res.IsError {
		t.Fatalf("reading an empty list errored: %+v (%v)", res, err)
	}
}

// An absent "tasks" field is a malformed call, NOT "clear the list" — a
// single dropped field must not silently wipe the model's plan. An
// explicitly empty array still clears (see the test above).
func TestTaskWrite_AbsentTasksFieldDoesNotClearTheList(t *testing.T) {
	f := NewTaskFamily()
	writeTasks(t, f, []Task{{Content: "important plan", Status: TaskInProgress}})

	res, err := f.Execute(context.Background(),
		Call{Name: contracts.ToolTaskWrite, Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("a task.write with no \"tasks\" field was accepted")
	}
	if got := f.Tasks(); len(got) != 1 || got[0].Content != "important plan" {
		t.Fatalf("the existing list was wiped by a malformed call: %+v", got)
	}
}

func TestTaskFamily_BadArgsAndUnknownTool(t *testing.T) {
	f := NewTaskFamily()
	res, err := f.Execute(context.Background(), Call{Name: contracts.ToolTaskWrite, Args: json.RawMessage(`not json`)})
	if err != nil || !res.IsError {
		t.Fatalf("malformed args = (%+v, %v); want a clean error result", res, err)
	}
	res, err = f.Execute(context.Background(), Call{Name: "task.bogus", Args: json.RawMessage(`{}`)})
	if err != nil || !res.IsError {
		t.Fatalf("unknown tool = (%+v, %v); want a clean error result", res, err)
	}
}

// Tasks() must hand back a copy — a caller mutating the returned slice must
// not reach into the family's state.
func TestTaskFamily_TasksReturnsACopy(t *testing.T) {
	f := NewTaskFamily()
	writeTasks(t, f, []Task{{Content: "original", Status: TaskPending}})

	got := f.Tasks()
	got[0].Content = "mutated"

	if f.Tasks()[0].Content != "original" {
		t.Fatal("mutating the slice returned by Tasks() changed the family's state")
	}
}

func TestTaskFamily_HandlesAndSpecs(t *testing.T) {
	f := NewTaskFamily()
	if f.Name() != contracts.FamilyTask {
		t.Errorf("Name = %q; want %q", f.Name(), contracts.FamilyTask)
	}
	for _, n := range []string{contracts.ToolTaskWrite, contracts.ToolTaskRead} {
		if !f.Handles(n) {
			t.Errorf("does not handle %s", n)
		}
	}
	if f.Handles(ToolRunCommand) {
		t.Error("claims to handle run_command")
	}
	if got := len(f.Specs()); got != 2 {
		t.Errorf("Specs returned %d; want 2", got)
	}
}

// Bookkeeping must not be gated behind an approval prompt — a model has to
// be able to track its own work under headless presets.
func TestClassify_TaskToolsAreReadKind(t *testing.T) {
	for _, name := range []string{contracts.ToolTaskWrite, contracts.ToolTaskRead} {
		kind, payload := Classify(Call{Name: name, Args: json.RawMessage(`{}`)}, Roots{WorkingDir: t.TempDir()})
		if kind != contracts.KindRead {
			t.Errorf("Classify(%s) kind = %q; want %q", name, kind, contracts.KindRead)
		}
		if _, ok := payload.(ReadPayload); !ok {
			t.Errorf("Classify(%s) payload is %T; want ReadPayload", name, payload)
		}
	}
}
