package toolrunner

import (
	"context"
	"strings"
	"testing"
)

func newExecFamily(t *testing.T) *ExecFamily {
	t.Helper()
	return NewExecFamily(newTestRoots(t))
}

func TestExecFamilyNameAndHandles(t *testing.T) {
	fam := newExecFamily(t)
	if fam.Name() != "exec" {
		t.Fatalf("Name() = %q", fam.Name())
	}
	if !fam.Handles(ToolRunCommand) {
		t.Error("Handles(run_command) = false, want true")
	}
	if fam.Handles(ToolReadFile) {
		t.Error("Handles(read_file) = true, want false")
	}
}

func TestRunCommandSuccess(t *testing.T) {
	fam := newExecFamily(t)
	res, err := fam.Execute(context.Background(), Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "echo hello"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got IsError: %+v", res)
	}
	if strings.TrimSpace(res.Content) != "hello" {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestRunCommandFailureIsError(t *testing.T) {
	fam := newExecFamily(t)
	res, err := fam.Execute(context.Background(), Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "exit 3"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for non-zero exit")
	}
}

func TestRunCommandTimeout(t *testing.T) {
	fam := newExecFamily(t)
	res, err := fam.Execute(context.Background(), Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "sleep 1", TimeoutMs: 50})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for timeout")
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Fatalf("content = %q, expected a timeout message", res.Content)
	}
}

func TestRunCommandBadArgs(t *testing.T) {
	fam := newExecFamily(t)
	res, err := fam.Execute(context.Background(), Call{Name: ToolRunCommand, Args: []byte(`{"command":""}`)})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for empty command")
	}
}

func TestRunCommandDefaultsToWorkingDirCwd(t *testing.T) {
	fam, roots := newExecFamilyWithRoots(t)
	res, err := fam.Execute(context.Background(), Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "pwd"})})
	if err != nil || res.IsError {
		t.Fatalf("pwd: err=%v res=%+v", err, res)
	}
	if strings.TrimSpace(res.Content) != roots.WorkingDir {
		t.Fatalf("cwd = %q, want %q", strings.TrimSpace(res.Content), roots.WorkingDir)
	}
}

func newExecFamilyWithRoots(t *testing.T) (*ExecFamily, Roots) {
	t.Helper()
	roots := newTestRoots(t)
	return NewExecFamily(roots), roots
}
