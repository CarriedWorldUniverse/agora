package toolrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// ToolRunCommand is the exec family's one tool.
const ToolRunCommand = "run_command"

// DefaultExecTimeout is run_command's timeout when timeout_ms is unset/<=0.
const DefaultExecTimeout = 120 * time.Second

// ExecFamily is the exec native tool family (agora-spec-mcp.md §5a):
// run_command executes a shell command with a timeout and captures its
// combined stdout/stderr. Sandbox/execpolicy enforcement is PARKED per the
// brief (agora-spec-io.md §3a: "enforcement mechanism ... remains parked") —
// this family only enforces the timeout and captures output; a later phase
// adds real sandboxing and wires the approval classifier's decision
// (Classify already reports every run_command as KindExec) to actually gate
// execution before it happens.
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
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()

	if runCtx.Err() == context.DeadlineExceeded {
		content := out.String()
		if content != "" {
			content += "\n"
		}
		content += fmt.Sprintf("command timed out after %s", timeout)
		return Result{Content: content, IsError: true}, nil
	}

	if runErr != nil {
		return Result{Content: out.String(), IsError: true}, nil
	}
	return Result{Content: out.String()}, nil
}
