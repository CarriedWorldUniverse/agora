package toolrunner

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func bgCall(t *testing.T, f *ExecFamily, name string, args any) Result {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.Execute(context.Background(), Call{Name: name, Args: b})
	if err != nil {
		t.Fatalf("Execute(%s): %v", name, err)
	}
	return res
}

func waitForBgOutput(t *testing.T, f *ExecFamily, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last Result
	for time.Now().Before(deadline) {
		last = bgCall(t, f, ToolBgOutput, map[string]string{"id": id})
		if strings.Contains(last.Content, want) {
			return last.Content
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("bg.output for %s never contained %q; last: %s", id, want, last.Content)
	return ""
}

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func TestRunBackground_StartsAndBgOutputSeesItsOutput(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	res := bgCall(t, f, ToolRunBackground, map[string]string{"command": "echo hello-from-bg"})
	if res.IsError {
		t.Fatalf("run_background errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "bg_1") {
		t.Fatalf("run_background did not return an id: %s", res.Content)
	}
	waitForBgOutput(t, f, "bg_1", "hello-from-bg")
}

// The defining property: run_background must NOT block waiting for the
// command to finish, unlike run_command.
func TestRunBackground_ReturnsImmediatelyEvenForALongCommand(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	start := time.Now()
	res := bgCall(t, f, ToolRunBackground, map[string]string{"command": "sleep 5"})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("run_background blocked for %v; must return immediately", elapsed)
	}
	if res.IsError {
		t.Fatalf("run_background errored: %s", res.Content)
	}
	// Clean up rather than leave a 5s sleep outliving the test.
	bgCall(t, f, ToolBgKill, map[string]string{"id": "bg_1"})
}

// bg.output is a DRAIN, not a re-read: a second call with no new output
// in between must not repeat what the first call already returned.
func TestBgOutput_OnlyReturnsNewOutputSinceLastRead(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	bgCall(t, f, ToolRunBackground, map[string]string{"command": "echo first; sleep 0.3; echo second"})
	waitForBgOutput(t, f, "bg_1", "first")

	second := waitForBgOutput(t, f, "bg_1", "second")
	if strings.Contains(second, "first") {
		t.Fatalf("second bg.output re-showed already-drained output: %s", second)
	}
}

func TestBgOutput_ReportsStatusAndExitCode(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	bgCall(t, f, ToolRunBackground, map[string]string{"command": "exit 3"})

	deadline := time.Now().Add(3 * time.Second)
	var out Result
	for time.Now().Before(deadline) {
		out = bgCall(t, f, ToolBgOutput, map[string]string{"id": "bg_1"})
		if strings.Contains(out.Content, "exited") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out.Content, "exited") || !strings.Contains(out.Content, "exit 3") {
		t.Fatalf("bg.output did not report exit status/code: %s", out.Content)
	}
}

func TestBgKill_StopsARunningJob(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	bgCall(t, f, ToolRunBackground, map[string]string{"command": "sleep 30"})

	job, ok := f.bg.get("bg_1")
	if !ok {
		t.Fatal("job not tracked")
	}
	// The process must be observably real and alive before we claim to
	// have killed it — otherwise "killed" could just mean "never started".
	deadline := time.Now().Add(2 * time.Second)
	for job.cmd.Process == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pid := job.cmd.Process.Pid
	if !alive(pid) {
		t.Fatal("job process not alive before kill — test cannot prove anything")
	}

	res := bgCall(t, f, ToolBgKill, map[string]string{"id": "bg_1"})
	if res.IsError {
		t.Fatalf("bg.kill errored: %s", res.Content)
	}

	deadline = time.Now().Add(2 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatalf("pid %d still alive after bg.kill", pid)
	}
}

// Killing an already-finished job must be a clean no-op, not an error —
// the caller almost always wants "make sure it's dead", which is already
// true.
func TestBgKill_AlreadyFinishedJobIsNotAnError(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	bgCall(t, f, ToolRunBackground, map[string]string{"command": "true"})
	waitForBgOutput(t, f, "bg_1", "") // let it finish; drains nothing but the poll loop settles

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _, _ := mustGet(t, f, "bg_1").snapshot(); s == backgroundExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	res := bgCall(t, f, ToolBgKill, map[string]string{"id": "bg_1"})
	if res.IsError {
		t.Fatalf("killing an already-finished job errored: %s", res.Content)
	}
}

func mustGet(t *testing.T, f *ExecFamily, id string) *backgroundJob {
	t.Helper()
	j, ok := f.bg.get(id)
	if !ok {
		t.Fatalf("job %s not found", id)
	}
	return j
}

func TestBgKill_UnknownIDIsAnError(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	res := bgCall(t, f, ToolBgKill, map[string]string{"id": "bg_999"})
	if !res.IsError {
		t.Fatal("bg.kill on an unknown id was accepted")
	}
}

func TestBgOutput_UnknownIDIsAnError(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	res := bgCall(t, f, ToolBgOutput, map[string]string{"id": "bg_999"})
	if !res.IsError {
		t.Fatal("bg.output on an unknown id was accepted")
	}
}

func TestBgList_ShowsAllJobs(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	if res := bgCall(t, f, ToolBgList, map[string]any{}); !strings.Contains(res.Content, "no background jobs") {
		t.Fatalf("empty list = %q; want the explicit empty state", res.Content)
	}
	bgCall(t, f, ToolRunBackground, map[string]string{"command": "sleep 30"})
	bgCall(t, f, ToolRunBackground, map[string]string{"command": "true"})

	deadline := time.Now().Add(2 * time.Second)
	res := bgCall(t, f, ToolBgList, map[string]any{})
	for time.Now().Before(deadline) && !strings.Contains(res.Content, "exited") {
		res = bgCall(t, f, ToolBgList, map[string]any{})
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(res.Content, "bg_1") || !strings.Contains(res.Content, "bg_2") {
		t.Fatalf("bg.list missing a job: %s", res.Content)
	}
	bgCall(t, f, ToolBgKill, map[string]string{"id": "bg_1"})
}

// The sliding-window guarantee: once buffered-but-unread output exceeds
// the cap, output keeps flowing (the NEWEST bytes survive) rather than
// freezing at whatever fit first — the property that makes bg.output safe
// to under-poll against a chatty long-running process. Tested directly
// against backgroundJob.Write, the precise unit that owns this behavior,
// rather than through a real process — no need to actually generate
// hundreds of KB of subprocess output to prove a buffer-management rule.
func TestBackgroundJob_OutputSlidesRatherThanFreezes(t *testing.T) {
	j := &backgroundJob{id: "bg_test", status: backgroundRunning}

	firstByte := []byte("A")
	if _, err := j.Write(firstByte); err != nil {
		t.Fatal(err)
	}
	// Overflow the cap with a distinct, findable marker at the very end.
	filler := make([]byte, backgroundOutputCap)
	for i := range filler {
		filler[i] = 'x'
	}
	if _, err := j.Write(filler); err != nil {
		t.Fatal(err)
	}
	marker := []byte("LATEST")
	if _, err := j.Write(marker); err != nil {
		t.Fatal(err)
	}

	got := j.drain()
	if len(got) > backgroundOutputCap {
		t.Fatalf("drained %d bytes; want capped at %d", len(got), backgroundOutputCap)
	}
	if !strings.HasSuffix(got, "LATEST") {
		t.Fatal("the newest write did not survive the cap — output froze instead of sliding")
	}
	if strings.HasPrefix(got, "A") {
		t.Fatal("the oldest byte survived the cap — window did not slide, it just grew then truncated from the end")
	}
}

func TestRunBackground_BadArgs(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	for _, args := range []string{`{}`, `{"command":""}`, `{"command":"   "}`, `not json`} {
		res, err := f.Execute(context.Background(), Call{Name: ToolRunBackground, Args: json.RawMessage(args)})
		if err != nil {
			t.Fatalf("Execute(%s): %v", args, err)
		}
		if !res.IsError {
			t.Errorf("args %s were accepted; want an error result", args)
		}
	}
}

func TestExecFamily_HandlesBackgroundTools(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	for _, name := range []string{ToolRunBackground, ToolBgOutput, ToolBgList, ToolBgKill} {
		if !f.Handles(name) {
			t.Errorf("does not handle %s", name)
		}
	}
	specs := f.Specs()
	if len(specs) != 5 { // run_command + 4 background tools
		t.Fatalf("Specs = %d; want 5", len(specs))
	}
}

// A killed job's exit code is always -1 (Go's convention for a
// signal-terminated process) and carries no information next to the word
// "killed" — showing it would read as a second, confusing signal.
func TestBgOutput_KilledJobDoesNotShowAMeaninglessExitCode(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	bgCall(t, f, ToolRunBackground, map[string]string{"command": "sleep 30"})
	bgCall(t, f, ToolBgKill, map[string]string{"id": "bg_1"})

	res := bgCall(t, f, ToolBgOutput, map[string]string{"id": "bg_1"})
	if !strings.Contains(res.Content, "killed") {
		t.Fatalf("status not reported as killed: %s", res.Content)
	}
	if strings.Contains(res.Content, "exit") {
		t.Fatalf("a killed job showed an exit code, which is always -1 and meaningless: %s", res.Content)
	}
}

// A natural exit's code IS meaningful and must still be shown.
func TestBgOutput_NaturalExitStillShowsCode(t *testing.T) {
	f := NewExecFamily(Roots{WorkingDir: t.TempDir()})
	bgCall(t, f, ToolRunBackground, map[string]string{"command": "exit 7"})

	deadline := time.Now().Add(2 * time.Second)
	var res Result
	for time.Now().Before(deadline) {
		res = bgCall(t, f, ToolBgOutput, map[string]string{"id": "bg_1"})
		if strings.Contains(res.Content, "exited") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(res.Content, "exit 7") {
		t.Fatalf("natural-exit code not shown: %s", res.Content)
	}
}
