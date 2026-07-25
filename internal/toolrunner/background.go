package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// background.go adds four tools to the exec family for the one thing
// run_command structurally cannot do: start something and keep working
// while it runs. run_command's Execute blocks until the process exits or
// times out — exactly right for "run the tests", wrong for "start the dev
// server, then keep editing against it". A dev loop needs the second
// shape, and until this file there was no way to get it: the only
// alternative was `cmd &`, which orphans the process the moment
// run_command's own timeout or process-group kill fires.
//
// Unix-only at runtime, same as run_command (see exec_procgroup_windows.go):
// this shells out to /bin/sh, which Windows has no equivalent for. It still
// builds on Windows (killProcGroup's Windows stub degrades to a direct-child
// kill, same as run_command's), but starting a job fails immediately with a
// clear exec error rather than silently misbehaving — disclosed here rather
// than left to be discovered from that error message alone.
//
// Deliberately a NEW file and a new set of tools rather than a
// `background: true` flag folded into run_command's Execute. That
// function is already dense, hard-won, and comment-annotated with
// process-group/WaitDelay/timeout reasoning specific to the SYNCHRONOUS
// case (see its own doc comments) — tangling a second, open-ended lifecycle
// into the same control flow would risk both. The two share the process-
// group helpers (setProcGroup/killProcGroup) and nothing else.

// Background job lifecycle tool names — package-local like ToolRunCommand
// and ToolWebFetch (both in this same package), not contracts.Tool*: those
// are for tools discoverable/relevant outside toolrunner, where this
// family's own tools have no callers beyond Classify and this file.
const (
	ToolRunBackground = "run_background"
	ToolBgOutput      = "bg.output"
	ToolBgList        = "bg.list"
	ToolBgKill        = "bg.kill"
)

// backgroundOutputCap bounds a job's buffered-but-unread output. Smaller
// than execOutputCap (the synchronous path's one-shot capture) because
// background output is meant to be polled incrementally, not captured
// once — and because several jobs can be running at once, where
// execOutputCap's larger budget is spent by exactly one call at a time.
// Once full, the OLDEST unread bytes are evicted to make room for the
// newest (a sliding window), not frozen at the first N bytes — a
// long-running dev server's later output matters more than its startup
// banner.
const backgroundOutputCap = 256 << 10 // 256 KiB

// maxTrackedBackgroundJobs bounds how many FINISHED jobs stay listed
// before the oldest is evicted to make room — defensive against a model
// spawning far more jobs than any real workflow needs. A still-RUNNING
// job is never evicted; only finished ones age out, and eviction only
// removes the job from bg.list/bg.output — it cannot resurrect an already-
// exited process, so there is nothing destructive about it.
const maxTrackedBackgroundJobs = 100

// backgroundStatus is a job's lifecycle state.
type backgroundStatus string

const (
	backgroundRunning backgroundStatus = "running"
	backgroundExited  backgroundStatus = "exited"
	backgroundKilled  backgroundStatus = "killed"
)

// backgroundJob is one tracked background process.
type backgroundJob struct {
	mu sync.Mutex

	id      string
	command string
	cmd     *exec.Cmd
	started time.Time

	buf    []byte // sliding-window output not yet drained by bg.output
	status backgroundStatus
	// exitErr/exitedAt are set once, when the process's own goroutine
	// observes it exit (naturally or via bg.kill). ExitCode is derived
	// from exitErr at read time rather than stored separately.
	exitErr  error
	exitedAt time.Time
}

func (j *backgroundJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.buf = append(j.buf, p...)
	if over := len(j.buf) - backgroundOutputCap; over > 0 {
		j.buf = j.buf[over:] // slide the window: drop oldest, keep newest
	}
	return len(p), nil // never backpressure the child over our own cap
}

// drain returns everything buffered since the last drain and clears it —
// bg.output's "only new output" semantics, so repeated polling doesn't
// re-show what was already read.
func (j *backgroundJob) drain() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := string(j.buf)
	j.buf = j.buf[:0]
	return s
}

func (j *backgroundJob) snapshot() (status backgroundStatus, exitCode int, running time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	status = j.status
	if status == backgroundRunning {
		running = time.Since(j.started)
	} else {
		running = j.exitedAt.Sub(j.started)
	}
	exitCode = -1
	if status != backgroundRunning {
		if j.exitErr == nil {
			exitCode = 0
		} else if ee, ok := j.exitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	return status, exitCode, running
}

// backgroundRegistry owns every job an ExecFamily has started, keyed by a
// short sequential id (bg_1, bg_2, ... — scoped to this registry/session,
// not globally unique, matching the turn/tool-call id convention elsewhere
// in this package).
type backgroundRegistry struct {
	mu     sync.Mutex
	next   int
	jobs   map[string]*backgroundJob
	order  []string // insertion order, for eviction and stable bg.list output
	roots  Roots
	closed bool
}

func newBackgroundRegistry(roots Roots) *backgroundRegistry {
	return &backgroundRegistry{jobs: make(map[string]*backgroundJob), roots: roots}
}

// start launches command detached from the caller and returns its id
// immediately — it does not wait for the process to exit or produce any
// output.
func (r *backgroundRegistry) start(command, cwd string) (*backgroundJob, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("background: session is shutting down, refusing to start %q", command)
	}
	r.next++
	id := "bg_" + strconv.Itoa(r.next)
	r.mu.Unlock()

	if cwd == "" {
		cwd = r.roots.WorkingDir
	}

	// Plain exec.Command, not CommandContext — a background job's lifetime
	// is NOT a deadline, it is "until bg.kill or session end", enforced
	// explicitly by killProcGroup (kill) and closeAll (session end), never
	// by context cancellation. Using CommandContext with a Background()
	// context that never fires would imply a cancellation path that isn't
	// there.
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = cwd
	setProcGroup(cmd) // same helper run_command uses — a killed job's whole tree dies, not just the shell

	job := &backgroundJob{id: id, command: command, started: time.Now(), status: backgroundRunning}
	cmd.Stdout = job
	cmd.Stderr = job
	job.cmd = cmd

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("background: starting %q: %w", command, err)
	}

	r.mu.Lock()
	r.jobs[id] = job
	r.order = append(r.order, id)
	r.evictOldestFinishedLocked()
	r.mu.Unlock()

	// Reap in the background so the process doesn't become a zombie once
	// it exits — the exact class of bug found and fixed for MCP subprocess
	// teardown earlier this session (Close-ordering, PID-liveness
	// verified): a started process that nothing ever calls Wait() on
	// leaves a zombie behind it even after this whole session ends.
	go func() {
		err := cmd.Wait()
		job.mu.Lock()
		if job.status == backgroundRunning { // bg.kill may have already marked it killed
			job.status = backgroundExited
		}
		job.exitErr = err
		job.exitedAt = time.Now()
		job.mu.Unlock()
	}()

	return job, nil
}

// evictOldestFinishedLocked drops the oldest FINISHED job once tracked
// jobs exceed maxTrackedBackgroundJobs. Caller holds r.mu. A running job
// is never a candidate — only bg.kill or the process exiting on its own
// makes one eligible.
func (r *backgroundRegistry) evictOldestFinishedLocked() {
	for len(r.order) > maxTrackedBackgroundJobs {
		evicted := false
		for i, id := range r.order {
			j := r.jobs[id]
			j.mu.Lock()
			finished := j.status != backgroundRunning
			j.mu.Unlock()
			if finished {
				delete(r.jobs, id)
				r.order = append(r.order[:i], r.order[i+1:]...)
				evicted = true
				break
			}
		}
		if !evicted {
			return // every tracked job is still running; nothing safe to drop
		}
	}
}

func (r *backgroundRegistry) get(id string) (*backgroundJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}

// list returns every tracked job in start order.
func (r *backgroundRegistry) list() []*backgroundJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*backgroundJob, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.jobs[id])
	}
	return out
}

// kill terminates a job's whole process group. Killing an already-
// finished job is a no-op, not an error — the caller almost always wants
// "make sure it's dead", and a job that already exited on its own already
// satisfies that.
func (r *backgroundRegistry) kill(id string) (found bool, err error) {
	j, ok := r.get(id)
	if !ok {
		return false, nil
	}
	j.mu.Lock()
	running := j.status == backgroundRunning
	if running {
		j.status = backgroundKilled
	}
	cmd := j.cmd
	j.mu.Unlock()
	if !running {
		return true, nil
	}
	return true, killProcGroup(cmd)
}

// closeAll kills every still-running job — the session-teardown path
// (Surface.Close, wired into Manager.Run the same way MCP subprocess
// teardown is). Without this, a background dev server outlives the agora
// session that started it: nothing else in either the TUI/pipe or daemon
// lane has a hook to reach a process this registry alone knows about.
// Marks the registry closed so no new job can start after teardown begins
// (a job started during shutdown would itself leak, since nothing would
// ever call closeAll again for it).
func (r *backgroundRegistry) closeAll() {
	r.mu.Lock()
	r.closed = true
	ids := append([]string(nil), r.order...)
	r.mu.Unlock()
	for _, id := range ids {
		_, _ = r.kill(id)
	}
}

// --- tool surface ---

var backgroundToolNames = map[string]bool{
	ToolRunBackground: true,
	ToolBgOutput:      true,
	ToolBgList:        true,
	ToolBgKill:        true,
}

func backgroundSpecs() []contracts.ToolSpec {
	return []contracts.ToolSpec{
		{
			Name: ToolRunBackground,
			Description: "Start a long-running command (a dev server, a watcher) WITHOUT waiting for it " +
				"to finish. Returns immediately with a job id. Use bg.output to read its output, bg.kill " +
				"to stop it. For anything that finishes on its own in a normal amount of time, use " +
				"run_command instead — it's simpler and returns the result directly.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string"},
					"cwd":     map[string]any{"type": "string", "description": "Working directory (default: session working dir)."},
				},
				"required": []string{"command"},
			}),
		},
		{
			Name: ToolBgOutput,
			Description: "Read a background job's output produced since the last bg.output call for it " +
				"(not the whole history — call bg.output again to see what's new), plus its status.",
			InputSchema: mustSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": map[string]any{"type": "string"}},
				"required":   []string{"id"},
			}),
		},
		{
			Name:        ToolBgList,
			Description: "List every background job started this session, with its status — use this if you've lost track of a job's id.",
			InputSchema: mustSchema(map[string]any{"type": "object", "properties": map[string]any{}}),
		},
		{
			Name:        ToolBgKill,
			Description: "Stop a background job. A no-op (not an error) if it already finished on its own.",
			InputSchema: mustSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": map[string]any{"type": "string"}},
				"required":   []string{"id"},
			}),
		},
	}
}

type runBackgroundArgs struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

type bgIDArgs struct {
	ID string `json:"id"`
}

func (e *ExecFamily) executeBackground(_ context.Context, call Call) (Result, error) {
	switch call.Name {
	case ToolRunBackground:
		var a runBackgroundArgs
		if err := json.Unmarshal(call.Args, &a); err != nil || strings.TrimSpace(a.Command) == "" {
			return errorResult(fmt.Errorf("%w: run_background", ErrBadArgs)), nil
		}
		job, err := e.bg.start(a.Command, a.Cwd)
		if err != nil {
			return errorResult(err), nil
		}
		return Result{Content: fmt.Sprintf("started %s: %s", job.id, a.Command)}, nil

	case ToolBgOutput:
		var a bgIDArgs
		if err := json.Unmarshal(call.Args, &a); err != nil || a.ID == "" {
			return errorResult(fmt.Errorf("%w: bg.output", ErrBadArgs)), nil
		}
		job, ok := e.bg.get(a.ID)
		if !ok {
			return errorResult(fmt.Errorf("bg.output: no job %q", a.ID)), nil
		}
		out := job.drain()
		status, exitCode, running := job.snapshot()
		return Result{Content: renderBgOutput(job.id, status, exitCode, running, out)}, nil

	case ToolBgList:
		jobs := e.bg.list()
		if len(jobs) == 0 {
			return Result{Content: "(no background jobs)"}, nil
		}
		return Result{Content: renderBgList(jobs)}, nil

	case ToolBgKill:
		var a bgIDArgs
		if err := json.Unmarshal(call.Args, &a); err != nil || a.ID == "" {
			return errorResult(fmt.Errorf("%w: bg.kill", ErrBadArgs)), nil
		}
		found, err := e.bg.kill(a.ID)
		if !found {
			return errorResult(fmt.Errorf("bg.kill: no job %q", a.ID)), nil
		}
		if err != nil {
			return errorResult(fmt.Errorf("bg.kill: %w", err)), nil
		}
		return Result{Content: a.ID + ": killed"}, nil

	default:
		return errorResult(fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)), nil
	}
}

func renderBgOutput(id string, status backgroundStatus, exitCode int, running time.Duration, out string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", id, status)
	// Only shown for a NATURAL exit — a killed job's "exit code" is always
	// -1 (Go's own convention for a signal-terminated process: no numeric
	// code applies), which next to the word "killed" reads as a second,
	// confusing signal rather than information. "killed" already says why.
	if status == backgroundExited {
		fmt.Fprintf(&b, " (exit %d)", exitCode)
	}
	fmt.Fprintf(&b, ", running %s\n", running.Round(time.Second))
	if out == "" {
		b.WriteString("(no new output)")
	} else {
		b.WriteString(out)
	}
	return b.String()
}

func renderBgList(jobs []*backgroundJob) string {
	// Stable, readable order: running jobs first (most actionable), then
	// finished ones, each group by start order — not map order, which
	// list() already avoids, but worth keeping explicit here too since
	// it's the property a reader of this function relies on.
	sort.SliceStable(jobs, func(i, j int) bool {
		si, _, _ := jobs[i].snapshot()
		sj, _, _ := jobs[j].snapshot()
		return si == backgroundRunning && sj != backgroundRunning
	})
	var b strings.Builder
	for _, j := range jobs {
		status, exitCode, running := j.snapshot()
		fmt.Fprintf(&b, "%s: %s", j.id, status)
		if status == backgroundExited {
			fmt.Fprintf(&b, " (exit %d)", exitCode)
		}
		fmt.Fprintf(&b, ", running %s — %s\n", running.Round(time.Second), j.command)
	}
	return strings.TrimRight(b.String(), "\n")
}
