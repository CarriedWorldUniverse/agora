package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// task.go is the harness-intrinsic task list: somewhere for a model working
// a long, multi-step job to hold its plan. Without it, long tasks drift —
// the model re-derives what it was doing from the transcript each turn, and
// the operator has no compact view of progress.
//
// The list is SESSION state, not durable record. It lives in the family (one
// per Manager, so one per thread) and dies with the process, the same
// lifetime as the conversation it describes. Persisting it would imply a
// resumed thread should resume its half-finished checklist, which is a
// bigger claim than this unit wants to make — the transcript already
// carries the history, and the model can rewrite the list on resume.
//
// Visibility comes for free: a task.write call is an ordinary tool call, so
// it surfaces in the transcript as an item like any other, and the result
// this returns IS the rendered checklist. No new event type needed.

// Task statuses. Deliberately three — a fourth ("blocked", "cancelled")
// invites the model to park work rather than resolve or drop it.
const (
	TaskPending    = "pending"
	TaskInProgress = "in_progress"
	TaskCompleted  = "completed"
)

var validTaskStatus = map[string]bool{
	TaskPending: true, TaskInProgress: true, TaskCompleted: true,
}

var taskToolNames = map[string]bool{
	contracts.ToolTaskWrite: true,
	contracts.ToolTaskRead:  true,
}

// Task is one entry in the list.
type Task struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// TaskFamily serves task.write and task.read over a per-session list.
type TaskFamily struct {
	mu    sync.Mutex
	tasks []Task
}

// NewTaskFamily builds the task family with an empty list.
func NewTaskFamily() *TaskFamily { return &TaskFamily{} }

func (f *TaskFamily) Name() string { return contracts.FamilyTask }

func (f *TaskFamily) Handles(name string) bool { return taskToolNames[name] }

func (f *TaskFamily) Specs() []contracts.ToolSpec {
	return []contracts.ToolSpec{
		{
			Name: contracts.ToolTaskWrite,
			Description: "Record the task list for multi-step work. Replaces the whole list — " +
				"send every task each time, not just changed ones. Exactly one task may be " +
				"in_progress. Use for work with several distinct steps; skip it for single-step requests.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tasks": map[string]any{
						"type":        "array",
						"description": "The complete task list, in order.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"content": map[string]any{"type": "string", "description": "What the step is."},
								"status": map[string]any{
									"type": "string",
									"enum": []string{TaskPending, TaskInProgress, TaskCompleted},
								},
							},
							"required": []string{"content", "status"},
						},
					},
				},
				"required": []string{"tasks"},
			}),
		},
		{
			Name: contracts.ToolTaskRead,
			Description: "Read back the current task list — useful after a long stretch of work " +
				"when the earlier list may have fallen out of context.",
			InputSchema: mustSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}),
		},
	}
}

// taskWriteArgs uses a POINTER for Tasks so an absent "tasks" field is
// distinguishable from an explicitly empty one. They mean very different
// things: `{"tasks": []}` is "the work is done, clear the list", while `{}`
// is a malformed call — and treating the malformed case as "clear" would
// silently wipe the model's plan on a single dropped field.
type taskWriteArgs struct {
	Tasks *[]Task `json:"tasks"`
}

func (f *TaskFamily) Execute(ctx context.Context, call Call) (Result, error) {
	switch call.Name {
	case contracts.ToolTaskRead:
		return Result{Content: f.render()}, nil

	case contracts.ToolTaskWrite:
		var a taskWriteArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return errorResult(fmt.Errorf("%w: task.write: %v", ErrBadArgs, err)), nil
		}
		if a.Tasks == nil {
			return errorResult(fmt.Errorf(
				"%w: task.write requires a \"tasks\" array (send [] to clear the list)", ErrBadArgs)), nil
		}
		if err := validateTasks(*a.Tasks); err != nil {
			// A validation failure is returned as an error RESULT, not a Go
			// error: the model reads it and corrects on the next call, which
			// is the whole point of stating the rule in the message.
			return errorResult(err), nil
		}
		// Swap and render under ONE lock hold, so the returned checklist is
		// exactly the list this call wrote rather than whatever a
		// concurrent write left behind.
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tasks = append([]Task(nil), *a.Tasks...) // copy: caller keeps no alias into our state
		return Result{Content: f.renderLocked()}, nil

	default:
		return errorResult(fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)), nil
	}
}

// validateTasks enforces the list's invariants. The one-in_progress rule is
// the load-bearing one: it forces the model to finish or explicitly park a
// step before starting another, which is what keeps a long task from
// fanning out into six half-done threads.
func validateTasks(tasks []Task) error {
	inProgress := 0
	for i, t := range tasks {
		if strings.TrimSpace(t.Content) == "" {
			return fmt.Errorf("task.write: task %d has empty content", i+1)
		}
		if !validTaskStatus[t.Status] {
			return fmt.Errorf("task.write: task %d has status %q; want one of %s, %s, %s",
				i+1, t.Status, TaskPending, TaskInProgress, TaskCompleted)
		}
		if t.Status == TaskInProgress {
			inProgress++
		}
	}
	if inProgress > 1 {
		return fmt.Errorf("task.write: %d tasks are in_progress; exactly one may be — "+
			"finish or set back to pending before starting another", inProgress)
	}
	return nil
}

// render draws the list as a checklist, and is what both tools return so
// the model always sees the resulting state rather than a bare "ok".
func (f *TaskFamily) render() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renderLocked()
}

// renderLocked is render's body, for callers already holding f.mu.
func (f *TaskFamily) renderLocked() string {
	if len(f.tasks) == 0 {
		return "(no tasks)"
	}
	var b strings.Builder
	done := 0
	for _, t := range f.tasks {
		switch t.Status {
		case TaskCompleted:
			b.WriteString("[x] ")
			done++
		case TaskInProgress:
			b.WriteString("[>] ")
		default:
			b.WriteString("[ ] ")
		}
		b.WriteString(t.Content)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n%d/%d done", done, len(f.tasks))
	return b.String()
}

// Tasks returns a copy of the current list, for callers that want to render
// it outside the tool path (a status line, a /tasks command).
func (f *TaskFamily) Tasks() []Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Task(nil), f.tasks...)
}
