package toolrunner

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestExecFamily_Close_KillsRunningBackgroundJobs is the same class of
// proof the MCP subprocess teardown fix needed earlier this session: not
// "Close() didn't error", but a real PID observed alive, then observed
// dead, after Close() returns. Without this, a background job (a dev
// server, by design meant to run indefinitely) outlives the agora session
// that started it — a leaked process is a much worse failure than a
// leaked goroutine.
func TestExecFamily_Close_KillsRunningBackgroundJobs(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	args, _ := json.Marshal(map[string]string{"command": "sleep 30"})
	res, err := f.Execute(context.Background(), Call{Name: ToolRunBackground, Args: args})
	if err != nil || res.IsError {
		t.Fatalf("run_background: %v / %s", err, res.Content)
	}

	job, ok := f.bg.get("bg_1")
	if !ok {
		t.Fatal("job not tracked")
	}
	deadline := time.Now().Add(2 * time.Second)
	for job.cmd.Process == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pid := job.cmd.Process.Pid
	if !alive(pid) {
		t.Fatal("job process not alive before Close — test cannot prove anything")
	}

	f.Close()

	deadline = time.Now().Add(2 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatalf("pid %d still alive after ExecFamily.Close()", pid)
	}
}

// A job started AFTER Close() begins must not itself leak — otherwise a
// race between shutdown and a straggling tool call would defeat the whole
// guarantee Close() exists to provide.
func TestExecFamily_Close_RefusesNewJobsAfterward(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	f.Close()

	args, _ := json.Marshal(map[string]string{"command": "true"})
	res, err := f.Execute(context.Background(), Call{Name: ToolRunBackground, Args: args})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("run_background succeeded after Close() — a straggling job would leak with nothing left to reap it")
	}
}

// Close() must be a clean no-op when nothing was ever started — the
// overwhelmingly common case (most sessions never call run_background).
func TestExecFamily_Close_NoJobsIsANoOp(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	f.Close() // must not panic or hang
}

// An already-finished job must not confuse Close() — it has nothing to
// kill and must not error or block on it.
func TestExecFamily_Close_SkipsAlreadyFinishedJobs(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	args, _ := json.Marshal(map[string]string{"command": "true"})
	if _, err := f.Execute(context.Background(), Call{Name: ToolRunBackground, Args: args}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _, _ := mustGet(t, f, "bg_1").snapshot(); s == backgroundExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.Close() // must not hang or error on an already-dead job
}
