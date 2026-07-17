package toolrunner

import (
	"context"
	"strings"
	"testing"
	"time"
)

// execWithDeadline runs fam.Execute in a goroutine and fails the test
// (rather than hanging the suite) if it doesn't return within bound —
// review finding #1: a backgrounded grandchild holding the output pipe, or
// a killed-but-un-reaped child, must never hang Execute forever.
func execWithDeadline(t *testing.T, fam *ExecFamily, call Call, bound time.Duration) Result {
	t.Helper()
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := fam.Execute(context.Background(), call)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("unexpected Go error: %v", o.err)
		}
		return o.res
	case <-time.After(bound):
		t.Fatalf("Execute did not return within %s — hung on an orphaned/backgrounded child", bound)
		return Result{}
	}
}

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

// --- review fix 1: timeout escapable + orphaned processes ---

// TestRunCommandBackgroundedChildDoesNotHang: the foreground shell
// backgrounds a child and exits almost immediately, but the backgrounded
// child still holds the inherited stdout/stderr pipe open. Without
// Setpgid+WaitDelay, Execute blocks forever waiting for pipe EOF even
// though the timeout has long since passed and the foreground command
// exited cleanly.
func TestRunCommandBackgroundedChildDoesNotHang(t *testing.T) {
	fam := newExecFamily(t)
	call := Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "sleep 5 &", TimeoutMs: 300})}
	res := execWithDeadline(t, fam, call, 3*time.Second)
	_ = res // returning at all (within the bound) is the fix; content is secondary
}

// TestRunCommandGrandchildKilled: a nested shell means the timed-out
// process has a live grandchild; without a process-group kill, killing
// only the direct child leaves the grandchild running and can still hang
// Execute on the inherited pipe.
func TestRunCommandGrandchildKilled(t *testing.T) {
	fam := newExecFamily(t)
	call := Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "sh -c 'sleep 5'", TimeoutMs: 100})}
	res := execWithDeadline(t, fam, call, 3*time.Second)
	if !res.IsError {
		t.Fatal("expected IsError for a killed grandchild-holding command")
	}
}

// TestRunCommandNoFalsePositiveNearDeadline: a fast command comfortably
// inside its timeout budget must report success, never a spurious timeout
// — the defect the brief calls out with checking runCtx.Err() after Run()
// returns, which races independently of whether the command actually
// finished in time.
func TestRunCommandNoFalsePositiveNearDeadline(t *testing.T) {
	fam := newExecFamily(t)
	res := execWithDeadline(t, fam, Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "true", TimeoutMs: 500})}, 2*time.Second)
	if res.IsError {
		t.Fatalf("expected success, got IsError: %+v", res)
	}
	if strings.Contains(res.Content, "timed out") {
		t.Fatalf("spurious timeout report: %+v", res)
	}
}

// TestRunCommandParentCancelNotMislabeledTimeout: when the PARENT ctx is
// cancelled (session shutdown / interrupt) while a command runs, it must be
// reported as cancelled, not "timed out" (delta-review LOW: Cancel fires for
// any runCtx.Done(), so the timed-out branch must distinguish the cause).
func TestRunCommandParentCancelNotMislabeledTimeout(t *testing.T) {
	fam := newExecFamily(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	res, err := fam.Execute(ctx, Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "sleep 5", TimeoutMs: 60000})})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError on cancellation, got: %+v", res)
	}
	if strings.Contains(res.Content, "timed out") {
		t.Fatalf("parent cancellation mislabeled as timeout: %+v", res)
	}
	if !strings.Contains(res.Content, "cancelled") {
		t.Fatalf("expected 'cancelled' in message, got: %+v", res)
	}
}

// --- review fix 4 (exec half): unbounded output cap ---

func TestRunCommandOutputCapped(t *testing.T) {
	fam := newExecFamily(t)
	// Emit well over execOutputCap bytes without needing `yes`/`dd`
	// (keeps the test itself fast and portable): a shell loop.
	res := execWithDeadline(t, fam, Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{
		Command: "head -c 2000000 /dev/zero | tr '\\0' 'a'",
	})}, 5*time.Second)
	if len(res.Content) > execOutputCap+200 {
		t.Fatalf("content len = %d, expected capped near %d", len(res.Content), execOutputCap)
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Fatalf("expected a truncation note, content len=%d", len(res.Content))
	}
}
