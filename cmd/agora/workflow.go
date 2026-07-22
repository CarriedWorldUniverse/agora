// `agora workflow`: the CLI entry point onto the complete-but-previously-
// entryless internal/workflow starlark engine (agora-spec-workflows.md).
// Scope (per the build brief): `run` (fresh + `-resume`) and `list`. OUT of
// scope: the `/workflow` TUI verb, `workflow ps/watch` live-attach,
// budgets, nested workflows (agora-spec-workflows.md §7's own deferral
// list).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
	"github.com/CarriedWorldUniverse/agora/internal/workflow"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/provider/claudesdk"
	"github.com/google/uuid"
	"go.starlark.net/starlark"
)

// runWorkflowCmd is `agora workflow`'s own arg0-style dispatch (mirrors
// main.go's `daemon`/`doctor` handling): args is os.Args[2:], i.e. whatever
// follows "workflow" on the command line.
func runWorkflowCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "agora workflow: expected a subcommand (run, list)")
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "run":
		os.Exit(runWorkflowRun(rest, os.Stdout, os.Stderr, claudesdk.New()))
	case "list":
		os.Exit(runWorkflowList(rest, os.Stdout))
	default:
		fmt.Fprintf(os.Stderr, "agora workflow: unknown subcommand %q (want run, list)\n", sub)
		os.Exit(2)
	}
}

// defaultWorkflowRunDir is the run-dir root (agora-spec-workflows.md §4:
// "Run dir: ~/.agora/workflow-runs/<run_id>/").
func defaultWorkflowRunDir() string {
	return filepath.Join(userHomeOrDot(), ".agora", "workflow-runs")
}

// openWorkflowStore opens the contracts.ThreadStore backing both the run's
// own thread (ctx.question/ctx.approval's park target) and every
// ctx.agent()-spawned child thread — persistence.NewLocalStore, the same
// production store type cmd/agora/inprocess.go's newInProcessStore opens
// for the interactive TUI. Rooted at journalDir's PARENT (so the default
// -journal=~/.agora/workflow-runs puts the store at ~/.agora/threads,
// exactly where newInProcessStore already roots it) rather than always
// hard-coding the operator's real home: this makes -journal alone enough to
// fully sandbox a test run (a temp -journal dir gets its own temp thread
// store too, with zero risk of an e2e test writing into the operator's
// real ~/.agora).
func openWorkflowStore(journalDir string) (contracts.ThreadStore, error) {
	root := filepath.Dir(journalDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", root, err)
	}
	return persistence.NewLocalStore(root, persistence.Config{})
}

// runMeta is the run dir's own small header file: it exists alongside the
// journal because internal/workflow's journal.Entry (journal.go) carries no
// run-level identity at all (no workflow name, no overall status) — by
// design, Entry is purely the per-call replay index. §4 explicitly lists a
// run dir's contents as "script snapshot, args, journal, ..., final
// result" — runMeta plus the sibling run.star/args.json/result.json files
// below are exactly that snapshot, written by the CLI layer (this file),
// not by internal/workflow itself.
type runMeta struct {
	RunID     string          `json:"run_id"`
	Name      string          `json:"name"`
	Filename  string          `json:"filename"`
	Args      json.RawMessage `json:"args,omitempty"`
	StartedAt time.Time       `json:"started_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	// Status mirrors workflow.Status plus "running" for an in-flight/
	// crashed-mid-run attempt that never reached a terminal Outcome.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func runMetaPath(runDir string) string   { return filepath.Join(runDir, "run-meta.json") }
func runScriptPath(runDir string) string { return filepath.Join(runDir, "run.star") }
func runArgsPath(runDir string) string   { return filepath.Join(runDir, "args.json") }
func runResultPath(runDir string) string { return filepath.Join(runDir, "result.json") }

func readRunMeta(runDir string) (runMeta, error) {
	b, err := os.ReadFile(runMetaPath(runDir))
	if err != nil {
		return runMeta{}, err
	}
	var m runMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return runMeta{}, fmt.Errorf("decode %s: %w", runMetaPath(runDir), err)
	}
	return m, nil
}

func writeRunMeta(runDir string, m runMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(runMetaPath(runDir), b, 0o644)
}

// workflowNameFromScript extracts meta.name from a workflow_meta(...) call
// at the script's top level, for `list`'s display and the run dir's
// runMeta.Name — internal/workflow.Run parses the SAME call internally
// (engine.go's own predeclared "workflow_meta" builtin) but does not expose
// the parsed Meta on its Outcome, so this CLI layer does its own minimal
// top-level ExecFile to recover just the name, independent of (and never
// modifying) internal/workflow. Falls back to the script's base filename
// (sans .star) if workflow_meta was never called or errors — never fatal to
// `run`/`list`, since the real Run() call is what actually validates the
// script.
func workflowNameFromScript(script []byte, filename string) string {
	fallback := filenameToWorkflowName(filename)
	var name string
	predeclared := starlark.StringDict{
		"workflow_meta": starlark.NewBuiltin("workflow_meta", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var (
				n, d       string
				phases     starlark.Value = starlark.None
				argsSchema starlark.Value = starlark.None
			)
			if err := starlark.UnpackArgs("workflow_meta", args, kwargs,
				"name", &n, "description?", &d, "phases?", &phases, "args_schema?", &argsSchema,
			); err != nil {
				return nil, err
			}
			name = n
			return starlark.None, nil
		}),
	}
	th := &starlark.Thread{Name: "wf-meta-probe:" + filename}
	th.SetMaxExecutionSteps(1_000_000)
	if _, err := starlark.ExecFile(th, filename, script, predeclared); err != nil || name == "" {
		return fallback
	}
	return name
}

func filenameToWorkflowName(filename string) string {
	base := filepath.Base(filename)
	return base[:len(base)-len(filepath.Ext(base))]
}

// runIDPattern mirrors internal/workflow's own FileJournalStore runID
// allowlist (journal.go: single path component, no slashes/dots) — minted
// run ids must satisfy it or FileJournalStore.Save/Read reject them
// outright.
func newRunID() string {
	// uuid.NewString() is already [A-Za-z0-9-]+ (hex + hyphens), a strict
	// subset of workflow's runIDPattern.
	return "wfr-" + uuid.NewString()
}

// runWorkflowRun implements `agora workflow run`. provider is injected so
// tests can pass bridle/fake's scripted Provider instead of the real
// claude-sdk lane (claudesdk.New(), wired by runWorkflowCmd) — mirrors
// cmd/agora/inprocess.go's own provider-as-parameter shape for
// newInProcessBackend, just made an explicit function parameter here since
// this path has no interactive backend to hide it behind.
func runWorkflowRun(args []string, stdout, stderr io.Writer, provider bridle.Provider) int {
	fs := flag.NewFlagSet("agora workflow run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	argsJSON := fs.String("args", "{}", "JSON object bound to ctx.args / main()'s args parameter")
	journalDir := fs.String("journal", defaultWorkflowRunDir(), "run-dir root (spec §4: ~/.agora/workflow-runs)")
	resume := fs.String("resume", "", "resume an existing run id instead of starting a fresh one")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var (
		runID    string
		filename string
		script   []byte
		argsVal  any
		runDir   string
		meta     runMeta
	)

	if *resume != "" {
		runID = *resume
		runDir = filepath.Join(*journalDir, runID)
		var err error
		meta, err = readRunMeta(runDir)
		if err != nil {
			fmt.Fprintf(stderr, "agora workflow run -resume %s: %v\n", runID, err)
			return 1
		}
		script, err = os.ReadFile(runScriptPath(runDir))
		if err != nil {
			fmt.Fprintf(stderr, "agora workflow run -resume %s: read script snapshot: %v\n", runID, err)
			return 1
		}
		argsBytes, err := os.ReadFile(runArgsPath(runDir))
		if err != nil {
			fmt.Fprintf(stderr, "agora workflow run -resume %s: read args snapshot: %v\n", runID, err)
			return 1
		}
		if err := json.Unmarshal(argsBytes, &argsVal); err != nil {
			fmt.Fprintf(stderr, "agora workflow run -resume %s: decode args snapshot: %v\n", runID, err)
			return 1
		}
		filename = meta.Filename
		fmt.Fprintf(stderr, "agora: resuming workflow run %s (%s)\n", runID, meta.Name)
	} else {
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "agora workflow run: expected exactly one <file.star> argument (or -resume <run-id>)")
			return 2
		}
		filename = fs.Arg(0)
		var err error
		script, err = os.ReadFile(filename)
		if err != nil {
			fmt.Fprintf(stderr, "agora workflow run: read %s: %v\n", filename, err)
			return 1
		}
		if err := json.Unmarshal([]byte(*argsJSON), &argsVal); err != nil {
			fmt.Fprintf(stderr, "agora workflow run: decode -args: %v\n", err)
			return 2
		}
		runID = newRunID()
		runDir = filepath.Join(*journalDir, runID)
		name := workflowNameFromScript(script, filename)
		now := time.Now().UTC()
		meta = runMeta{
			RunID: runID, Name: name, Filename: filename,
			Args: json.RawMessage(*argsJSON), StartedAt: now, UpdatedAt: now,
			Status: "running",
		}
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			fmt.Fprintf(stderr, "agora workflow run: mkdir run dir: %v\n", err)
			return 1
		}
		if err := os.WriteFile(runScriptPath(runDir), script, 0o644); err != nil {
			fmt.Fprintf(stderr, "agora workflow run: snapshot script: %v\n", err)
			return 1
		}
		if err := os.WriteFile(runArgsPath(runDir), []byte(*argsJSON), 0o644); err != nil {
			fmt.Fprintf(stderr, "agora workflow run: snapshot args: %v\n", err)
			return 1
		}
		if err := writeRunMeta(runDir, meta); err != nil {
			fmt.Fprintf(stderr, "agora workflow run: write run-meta: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "agora: starting workflow run %s (%s)\n", runID, name)
	}

	store, err := openWorkflowStore(*journalDir)
	if err != nil {
		fmt.Fprintf(stderr, "agora workflow run: open thread store: %v\n", err)
		return 1
	}
	closeStore := func() {
		if c, ok := store.(io.Closer); ok {
			_ = c.Close()
		}
	}
	defer closeStore()

	threadID := "wf-" + runID
	cwd, _ := os.Getwd()
	if err := ensureThreadCreated(store, threadID, cwd); err != nil {
		fmt.Fprintf(stderr, "agora workflow run: create run thread: %v\n", err)
		return 1
	}

	invoker := buildWorkflowInvoker(store, provider, threadID, cwd)
	questions := &workflow.QuestionRouter{Log: planning.NewQuestionLog(store), Store: store}
	journal := workflow.NewFileJournalStore(*journalDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out, runErr := workflow.Run(ctx, workflow.RunOptions{
		RunID:     runID,
		ThreadID:  threadID,
		Script:    script,
		Filename:  filename,
		Args:      argsVal,
		Invoker:   invoker,
		Questions: questions,
		Journal:   journal,
		Identity:  "agora-workflow-cli",
	})

	meta.UpdatedAt = time.Now().UTC()
	if runErr != nil {
		meta.Status = "errored"
		meta.Error = runErr.Error()
		_ = writeRunMeta(runDir, meta)
		fmt.Fprintf(stderr, "agora workflow run: %v\n", runErr)
		return 1
	}

	switch out.Status {
	case workflow.StatusCompleted:
		meta.Status = "completed"
		_ = writeRunMeta(runDir, meta)
		_ = os.WriteFile(runResultPath(runDir), out.Result, 0o644)
		fmt.Fprintln(stdout, string(out.Result))
		return 0
	case workflow.StatusParked:
		meta.Status = "parked"
		_ = writeRunMeta(runDir, meta)
		fmt.Fprintf(stderr, "agora: run %s parked on %s %s — answer it, then re-run `agora workflow run -resume %s` (spec agora-spec-workflows.md §2: \"the RUN parks; no thread is held\")\n",
			runID, out.Parked.Kind, out.Parked.QuestionID, runID)
		return 0
	case workflow.StatusErrored:
		meta.Status = "errored"
		meta.Error = out.Err.Error()
		_ = writeRunMeta(runDir, meta)
		fmt.Fprintf(stderr, "agora workflow run: script error: %v\n", out.Err)
		return 1
	default:
		meta.Status = string(out.Status)
		_ = writeRunMeta(runDir, meta)
		fmt.Fprintf(stderr, "agora workflow run: unexpected outcome status %q\n", out.Status)
		return 1
	}
}

// runWorkflowList implements `agora workflow list`: every run dir under
// -journal, one line each (run id, workflow name, status, started time) —
// read from each run's runMeta (see the runMeta doc comment: the journal
// itself carries no run-level name/status).
func runWorkflowList(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("agora workflow list", flag.ContinueOnError)
	journalDir := fs.String("journal", defaultWorkflowRunDir(), "run-dir root (spec §4: ~/.agora/workflow-runs)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	entries, err := os.ReadDir(*journalDir)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "RUN_ID\tNAME\tSTATUS\tSTARTED")
		return 0
	}
	if err != nil {
		fmt.Fprintf(stdout, "agora workflow list: %v\n", err)
		return 1
	}

	type row struct {
		runID, name, status, started string
		startedAt                    time.Time
	}
	var rows []row
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(*journalDir, e.Name())
		m, merr := readRunMeta(runDir)
		if merr != nil {
			// A journal.jsonl with no run-meta.json (foreign/legacy dir) —
			// still list it, status/name unknown rather than dropping it
			// silently.
			rows = append(rows, row{runID: e.Name(), name: "(unknown)", status: "unknown"})
			continue
		}
		rows = append(rows, row{
			runID: m.RunID, name: m.Name, status: m.Status,
			started: m.StartedAt.Format(time.RFC3339), startedAt: m.StartedAt,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].startedAt.Before(rows[j].startedAt) })

	fmt.Fprintln(stdout, "RUN_ID\tNAME\tSTATUS\tSTARTED")
	for _, r := range rows {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", r.runID, r.name, r.status, r.started)
	}
	return 0
}
