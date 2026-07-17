package toolrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// ToolRunCommand is the exec family's one tool.
const ToolRunCommand = "run_command"

// DefaultExecTimeout is run_command's timeout when timeout_ms is unset/<=0.
const DefaultExecTimeout = 120 * time.Second

// execOutputCap bounds captured combined stdout+stderr so a command like
// `yes`/`dd` can't OOM the process (review fix 4).
const execOutputCap = 1 << 20 // 1 MiB

// execWaitDelay bounds how long Execute waits for a command's I/O pipes to
// close on their own — after a Cancel-triggered kill, OR after the
// foreground process has already exited on its own — before force-closing
// them (os/exec's Cmd.WaitDelay). Review fix 1: a command that backgrounds
// a child ("cmd &") exits itself almost immediately, but the backgrounded
// child still holds the inherited stdout/stderr pipe open; without a
// bounded WaitDelay, Execute hangs forever waiting for that pipe's last
// writer to close, timeout_ms notwithstanding (the foreground process
// already exited, so ctx's cancellation goroutine never even fires).
const execWaitDelay = 500 * time.Millisecond

// ExecFamily is the exec native tool family (agora-spec-mcp.md §5a):
// run_command executes a shell command with a timeout and captures its
// combined stdout/stderr. Sandbox/execpolicy enforcement is PARKED per the
// brief (agora-spec-io.md §3a: "enforcement mechanism ... remains parked") —
// this family only enforces the timeout and captures output; a later phase
// adds real sandboxing and wires the approval classifier's decision
// (Classify already reports every run_command as KindExec) to actually gate
// execution before it happens.
//
// run_command inherits the full parent os.Environ() (no scrub/allowlist) —
// left as-is deliberately for this unit; env-scrubbing is deferred to the
// parked exec sandbox/execpolicy hardening phase (agora-spec-io.md §3a),
// so it is a known, tracked gap rather than a silent one.
type ExecFamily struct {
	roots Roots
}

// NewExecFamily builds the exec family. roots supplies the default cwd
// (WorkingDir) when a call doesn't set one; unlike the fs family, cwd is
// NOT containment-checked here (sandboxing is parked, per above) — only
// defaulted.
func NewExecFamily(roots Roots) *ExecFamily {
	return &ExecFamily{roots: roots}
}

func (e *ExecFamily) Name() string { return contracts.FamilyExec }

func (e *ExecFamily) Handles(name string) bool { return name == ToolRunCommand }

func (e *ExecFamily) Specs() []contracts.ToolSpec {
	return []contracts.ToolSpec{
		{
			Name:        ToolRunCommand,
			Description: "Run a shell command and return its combined stdout/stderr.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":    map[string]any{"type": "string"},
					"cwd":        map[string]any{"type": "string", "description": "Working directory for the command (default: session working dir)."},
					"timeout_ms": map[string]any{"type": "integer", "description": "Max time to allow the command to run, in milliseconds (default 120000)."},
				},
				"required": []string{"command"},
			}),
		},
	}
}

type runCommandArgs struct {
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	TimeoutMs int    `json:"timeout_ms"`
}

func (e *ExecFamily) Execute(ctx context.Context, call Call) (Result, error) {
	if call.Name != ToolRunCommand {
		return errorResult(fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)), nil
	}

	var a runCommandArgs
	if err := json.Unmarshal(call.Args, &a); err != nil || a.Command == "" {
		return errorResult(fmt.Errorf("%w: run_command", ErrBadArgs)), nil
	}

	timeout := DefaultExecTimeout
	if a.TimeoutMs > 0 {
		timeout = time.Duration(a.TimeoutMs) * time.Millisecond
	}
	cwd := a.Cwd
	if cwd == "" {
		cwd = e.roots.WorkingDir
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", a.Command)
	cmd.Dir = cwd

	// Setpgid makes this child the leader of its OWN new process group, so
	// a negative-pid kill below reaches every descendant (grandchildren
	// like a backgrounded `sleep &`, or a nested `sh -c 'sleep 5'`) rather
	// than only the direct /bin/sh child — review fix 1's orphan-on-timeout
	// defect.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// WaitDelay bounds Execute against BOTH known hang sources (os/exec
	// doc): a child that fails to exit after ctx is canceled, and a child
	// that exits but leaves inherited I/O pipes open (the backgrounded-
	// grandchild case — the foreground process may already be gone by the
	// time WaitDelay's force-close kicks in, so this is not just a
	// belt-and-suspenders duplicate of the Cancel kill below).
	cmd.WaitDelay = execWaitDelay

	// timedOut is set from inside Cancel, which os/exec invokes only when
	// ctx.Done() fires while THIS command is still outstanding (not once
	// Wait has already observed the process exit) — so it is immune to
	// the false-positive race the brief calls out for checking
	// runCtx.Err() after Run() returns (a command that finishes right at
	// the deadline, and whose exit was already reaped, never triggers
	// Cancel at all). The brief's suggested `errors.Is(runErr,
	// context.DeadlineExceeded)` check does not hold here: os/exec only
	// wraps the context error when the process's own exit is a SUCCESS
	// after Cancel runs (Cmd.Cancel doc); a process-group SIGKILL always
	// yields a non-success ("signal: killed") exit, so that specific
	// wrapping is structurally unreachable together with the real
	// group-kill this fix requires (verified empirically against Go
	// 1.26's os/exec, not just from the doc prose). This flag is the
	// equivalent-but-actually-correct signal.
	var timedOut atomic.Bool
	cmd.Cancel = func() error {
		timedOut.Store(true)
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	out := newCappedWriter(execOutputCap)
	cmd.Stdout = out
	cmd.Stderr = out // same writer instance: os/exec serializes Write calls between them (Cmd.Stdout doc)

	runErr := cmd.Run()

	switch {
	case timedOut.Load():
		content := out.String()
		if content != "" {
			content += "\n"
		}
		content += fmt.Sprintf("command timed out after %s", timeout)
		return Result{Content: content, IsError: true}, nil

	case errors.Is(runErr, exec.ErrWaitDelay):
		// The foreground process exited on its own (success or not) but
		// left a backgrounded child holding stdout/stderr open; WaitDelay
		// force-closed the pipes so Execute didn't hang. Output up to that
		// point is real, just possibly incomplete.
		content := out.String()
		if content != "" {
			content += "\n"
		}
		content += "command exited but left a background process holding stdout/stderr open; output may be incomplete"
		return Result{Content: content, IsError: true}, nil

	case runErr != nil:
		return Result{Content: out.String(), IsError: true}, nil

	default:
		return Result{Content: out.String()}, nil
	}
}

// cappedWriter is an io.Writer that keeps at most cap bytes, silently
// discarding (not erroring — a child process must never see a write
// failure just because our cap kicked in) anything past that and noting
// the truncation once in String(). Review fix 4: an unbounded command like
// `yes`/`dd` must not let Execute's captured output grow without bound.
type cappedWriter struct {
	buf       []byte
	cap       int
	truncated bool
}

func newCappedWriter(cap int) *cappedWriter {
	return &cappedWriter{cap: cap}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if !w.truncated {
		remaining := w.cap - len(w.buf)
		if remaining <= 0 {
			w.truncated = true
		} else if len(p) > remaining {
			w.buf = append(w.buf, p[:remaining]...)
			w.truncated = true
		} else {
			w.buf = append(w.buf, p...)
		}
	}
	return len(p), nil // always report the full write consumed: never backpressure/EPIPE the child over our own cap
}

func (w *cappedWriter) String() string {
	if w.truncated {
		return string(w.buf) + fmt.Sprintf("\n[output truncated at %d bytes]", w.cap)
	}
	return string(w.buf)
}
