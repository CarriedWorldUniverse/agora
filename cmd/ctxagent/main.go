//go:build ctxmap_llama

// ctxagent — the real agentic-coding experiment. Drives bridle's production
// harness (real tool loop) with a sandbox ToolRunner and, for map-on configs,
// the ctxmap adapter (working memory + in-harness tool-result distiller).
// Agora orchestrates + scores; bridle executes with the same loop production
// uses. Four configs: {small, big} × {map-on, map-off}.
//
//	go build -tags ctxmap_llama -o ctxagent ./cmd/ctxagent
//	./ctxagent -task tasks/kvstore -reps 1
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/codemap"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/adapter"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/distill"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/embed"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/extractor"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/memory"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/render"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/store"
	"github.com/CarriedWorldUniverse/bridle/provider/openai"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// sandboxRunner implements bridle.ToolRunner: read/write/run scoped to a dir.
type sandboxRunner struct{ dir string }

var sandboxDbg = os.Getenv("CTXMAP_DEBUG") != ""

// jailAvailable: can we run commands in an unprivileged user+mount namespace?
// Probed once at startup; without it we run unjailed with a loud warning.
var jailAvailable = func() bool {
	if err := exec.Command("unshare", "-Umr", "true").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: unshare userns unavailable — run_command is UNJAILED (pristine task dirs are writable by escaped commands)")
		return false
	}
	return true
}()

// auditCommand appends every run_command to a persistent audit log —
// the incident post-mortem was blinded by a driver script's 2>/dev/null;
// the audit trail must not depend on how a run was launched.
func auditCommand(command string) {
	f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), ".ctxmap", "cmd-audit.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s | %s\n", time.Now().UTC().Format(time.RFC3339), command)
}

// taskIntegrityCheck verifies the pristine task dir is git-clean. Runs after
// every rep: if an agent ever corrupts the benchmark again, the campaign
// aborts loudly instead of silently producing invalid comparisons.
func taskIntegrityCheck(taskDir string) error {
	out, err := exec.Command("git", "-C", taskDir, "status", "--porcelain", "--", ".").CombinedOutput()
	if err != nil {
		return nil // not a git repo (external task dir) — gate not applicable
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return fmt.Errorf("PRISTINE TASK DIR DIRTY after rep:\n%s", s)
	}
	return nil
}

func (r *sandboxRunner) Run(_ context.Context, call bridle.ToolCall) (json.RawMessage, error) {
	var a struct {
		Path, Content, Command string
	}
	json.Unmarshal(call.Args, &a)
	if sandboxDbg {
		// host-side call log: works for map-off too (the adapter's trace only
		// exists map-on), so churn patterns are visible in every config
		arg := a.Path
		if call.Name == "run_command" {
			arg = a.Command
		}
		fmt.Fprintf(os.Stderr, "[sandbox] %s %s\n", call.Name, arg)
	}
	out := func(s string) (json.RawMessage, error) { b, _ := json.Marshal(s); return b, nil }
	safe := func(p string) (string, bool) {
		full := filepath.Clean(filepath.Join(r.dir, p))
		return full, strings.HasPrefix(full, r.dir)
	}
	switch call.Name {
	case "read_file":
		full, ok := safe(a.Path)
		if !ok {
			return out("error: path escapes sandbox")
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return out("error: " + err.Error())
		}
		return out(string(b))
	case "write_file":
		full, ok := safe(a.Path)
		if !ok {
			return out("error: path escapes sandbox")
		}
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(a.Content), 0o644); err != nil {
			return out("error: " + err.Error())
		}
		return out("OK wrote " + a.Path)
	case "run_command":
		auditCommand(a.Command)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		// JAIL (incident 2026-07-11): a flailing agent escaped via run_command
		// (bash is not path-jailed like read/write_file) and corrupted the
		// PRISTINE task dir mid-campaign, invalidating a rep batch. Commands now
		// run in their own mount namespace with ALL of $HOME read-only; only the
		// sandbox (under /tmp) is writable. Reads outside the sandbox remain
		// possible (full hiding needs a pivot_root jail) — the audit log makes
		// them visible, and the post-rep integrity gate catches anything missed.
		var cmd *exec.Cmd
		if jailAvailable {
			// inner shell is NON-login: .bashrc writes to ~/.cache, which is now
			// read-only — a login shell would prepend a spurious "Read-only file
			// system" warning to every tool result the model sees
			jail := `mount --bind "$HOME" "$HOME" && mount -o remount,bind,ro "$HOME" && cd "$1" && exec bash -c "$2"`
			cmd = exec.CommandContext(ctx, "unshare", "-Umr", "bash", "-c", jail, "jail", r.dir, a.Command)
		} else {
			cmd = exec.CommandContext(ctx, "bash", "-lc", a.Command)
			cmd.Dir = r.dir
		}
		b, _ := cmd.CombinedOutput()
		s := string(b)
		if len(s) > 8000 {
			s = s[:8000] + "\n[...truncated]"
		}
		return out(s)
	case "list_files":
		var files []string
		filepath.Walk(r.dir, func(p string, info os.FileInfo, _ error) error {
			if info != nil && !info.IsDir() {
				rel, _ := filepath.Rel(r.dir, p)
				files = append(files, rel)
			}
			return nil
		})
		return out(strings.Join(files, "\n"))
	}
	return out("unknown tool: " + call.Name)
}

// evictor forces the context-degradation regime: it keeps only the last `keep`
// tool results verbatim and replaces older ones with a stub. Message STRUCTURE
// is preserved (deleting messages breaks assistant-toolcall/tool-result pairing
// and produces API errors, not degradation) — only the INFORMATION is removed,
// which is exactly what a long real session does to early tool output. With a
// map-on engine, evicted content is offered for ingestion first (content-hash
// dedup makes this free if streaming ingestion already mined it).
type evictor struct {
	keep  int
	eng   *memory.Engine // nil = map-off
	focus string
}

const evictStub = "[tool result evicted: context budget exceeded; %d chars dropped]"

func (ev *evictor) hook(_ context.Context, in bridle.BeforeModelCallCtx) (bridle.BeforeModelCallCtx, bridle.HookAction, error) {
	if ev.keep <= 0 {
		return in, bridle.HookContinue, nil
	}
	msgs := in.Request.Messages
	var toolIdx []int
	for i, m := range msgs {
		if m.Role == "tool_result" && !strings.HasPrefix(m.Content, "[tool result evicted") {
			toolIdx = append(toolIdx, i)
		}
	}
	for n := 0; n < len(toolIdx)-ev.keep; n++ {
		i := toolIdx[n]
		if ev.eng != nil {
			name := msgs[i].ToolName
			if name == "" {
				name = "tool"
			}
			ev.eng.IngestToolResult(name, msgs[i].Content, ev.focus)
		}
		msgs[i].Content = fmt.Sprintf(evictStub, len(msgs[i].Content))
	}
	return in, bridle.HookContinue, nil
}

// wsetEvictor is arm F: working-set retention — eviction that works WITH the
// trained re-verify-via-tools tendency instead of against it (operator
// direction, 2026-07-15). Policy:
//   - read_file: the LATEST read of each distinct path is never evicted;
//     older reads of the SAME path are stubbed as superseded (the model's
//     instinct to re-check finds current truth already in context).
//   - everything else (run_command, list_files): keep the last `keep`
//     verbatim, stub older — history still ages out; only the working set
//     is retained.
//
// Context therefore scales with the size of the working set (files touched),
// not the length of the history — the property a real long session needs.
// Deterministic, zero internal AI.
type wsetEvictor struct {
	keep int
}

func (ev *wsetEvictor) hook(_ context.Context, in bridle.BeforeModelCallCtx) (bridle.BeforeModelCallCtx, bridle.HookAction, error) {
	msgs := in.Request.Messages
	// map tool-call id -> (name, path) from assistant tool_use blocks
	type callMeta struct{ name, path string }
	calls := map[string]callMeta{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			var args struct {
				Path string `json:"path"`
			}
			json.Unmarshal(tc.Args, &args)
			calls[tc.ID] = callMeta{tc.Name, args.Path}
		}
	}
	// newest read per path wins; find it in one reverse pass
	latestRead := map[string]int{} // path -> newest msg index holding a live read
	var otherIdx []int             // non-read_file live results, in order
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != "tool_result" || strings.HasPrefix(m.Content, "[tool result") {
			continue
		}
		c := calls[m.ToolCallID]
		if c.name == "read_file" && c.path != "" {
			if _, seen := latestRead[c.path]; !seen {
				latestRead[c.path] = i
			}
		}
	}
	for i, m := range msgs {
		if m.Role != "tool_result" || strings.HasPrefix(m.Content, "[tool result") {
			continue
		}
		c := calls[m.ToolCallID]
		if c.name == "read_file" && c.path != "" {
			if latestRead[c.path] != i {
				msgs[i].Content = fmt.Sprintf("[tool result superseded: a newer read of %s appears later in context; %d chars dropped]", c.path, len(m.Content))
			}
			continue
		}
		otherIdx = append(otherIdx, i)
	}
	for n := 0; n < len(otherIdx)-ev.keep; n++ {
		i := otherIdx[n]
		msgs[i].Content = fmt.Sprintf(evictStub, len(msgs[i].Content))
	}
	return in, bridle.HookContinue, nil
}

// journal is arm E: a GCC-shaped decision journal. The model checkpoints
// decisions/findings/rejected hypotheses via journal_commit; entries render
// into a system-prompt block that survives eviction — the only durable channel
// in the run. Zero internal AI: the model authors, the harness only persists.
// The disk-recoverable objection (arms B/C/D) does not apply here: the journey
// is the one part of a coding session that is NOT on disk.
type journal struct {
	mu      sync.Mutex
	entries []string
	baseSys string // host AppendSystemPrompt captured at step 0 (Request persists across steps; append-per-call would compound duplicates — the ctxmap adapter pattern)
}

const journalCap = 30 // 40-step runs never legitimately need more

func (j *journal) commit(milestone, notes string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.entries) >= journalCap {
		return "journal full — stop committing and finish the task"
	}
	e := strings.TrimSpace(milestone)
	if n := strings.TrimSpace(notes); n != "" {
		e += " — " + n
	}
	if len(e) > 700 {
		e = e[:700] + "…"
	}
	j.entries = append(j.entries, e)
	if dbg := os.Getenv("CTXMAP_DEBUG"); dbg != "" {
		fmt.Fprintf(os.Stderr, "[journal] commit %d: %s\n", len(j.entries), e)
	}
	return fmt.Sprintf("committed entry %d (journal persists across context eviction)", len(j.entries))
}

// block renders the protocol + entries. The protocol is ALWAYS present: the
// smoke rep showed a tool description alone gets zero uptake — the model needs
// a standing system-prompt instruction (the GCC paper wires its commands the
// same way).
func (j *journal) block() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var b strings.Builder
	b.WriteString("\n\n## Decision journal — MANDATORY protocol\n" +
		"Your context is aggressively evicted: older tool results become stubs and their information is GONE. " +
		"The journal below is the ONLY thing that survives. Therefore:\n" +
		"- IMMEDIATELY after fixing a bug, running the tests, or rejecting a hypothesis, call journal_commit (one line: what/where/outcome).\n" +
		"- BEFORE re-reading or re-investigating anything, check the journal — never redo work it already records.\n")
	if len(j.entries) == 0 {
		b.WriteString("\nJournal: (empty — nothing checkpointed yet)\n")
		return b.String()
	}
	b.WriteString("\nJournal:\n")
	for i, e := range j.entries {
		fmt.Fprintf(&b, "%d. %s\n", i+1, e)
	}
	return b.String()
}

func (j *journal) attach(h *bridle.Harness) {
	h.RegisterBeforeModelCall(func(_ context.Context, in bridle.BeforeModelCallCtx) (bridle.BeforeModelCallCtx, bridle.HookAction, error) {
		if in.Step == 0 {
			j.baseSys = in.Request.AppendSystemPrompt
			in.Request.Tools = append(in.Request.Tools, bridle.ToolDef{
				Name: "journal_commit",
				Description: "Checkpoint your investigation into a journal that SURVIVES context eviction (nothing else does). Call after every meaningful step: a bug found (file, symbol, what was wrong, the fix applied), a hypothesis REJECTED (so you never retry it), a test result. milestone = one line; notes = key details.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"milestone":{"type":"string"},"notes":{"type":"string"}},"required":["milestone"]}`),
			})
		}
		in.Request.AppendSystemPrompt = j.baseSys + j.block()
		if os.Getenv("CTXMAP_DEBUG") != "" && in.Step == 0 {
			fmt.Fprintf(os.Stderr, "[journal] attached: %d tools, sysappend=%dch\n", len(in.Request.Tools), len(in.Request.AppendSystemPrompt))
		}
		return in, bridle.HookContinue, nil
	})
	h.RegisterBeforeToolCall(func(_ context.Context, in bridle.BeforeToolCallCtx) (bridle.BeforeToolCallCtx, bridle.HookAction, error) {
		if in.Call.Name != "journal_commit" {
			return in, bridle.HookContinue, nil
		}
		var args struct{ Milestone, Notes string }
		json.Unmarshal(in.Call.Args, &args)
		res, _ := json.Marshal(j.commit(args.Milestone, args.Notes))
		in.Deny = true
		in.Result = res
		return in, bridle.HookContinue, nil
	})
	// Smoke reps 1-3: DeepSeek never calls a bookkeeping tool from a system-
	// prompt protocol alone (0 commits / 40 steps, wiring verified). The
	// pi-brain lesson: don't rely on the model reading the manual — put the
	// nudge where the model is looking. Append a reminder to every
	// run_command result (the natural checkpoint moment).
	h.RegisterAfterToolCall(func(_ context.Context, in bridle.AfterToolCallCtx) (bridle.AfterToolCallCtx, bridle.HookAction, error) {
		if in.Call.Name != "run_command" || in.Result.Err != "" {
			return in, bridle.HookContinue, nil
		}
		var cur string
		if json.Unmarshal(in.Result.Result, &cur) != nil {
			cur = string(in.Result.Result)
		}
		cur += "\n\n[journal reminder: this output will be EVICTED soon. journal_commit your conclusions from it NOW (bugs found/fixed, hypotheses rejected, test status).]"
		b, _ := json.Marshal(cur)
		in.Result.Result = b
		return in, bridle.HookContinue, nil
	})
}

// journalTaskProtocol is appended to the USER task prompt when -journal is on:
// smoke reps showed the system-prompt protocol alone gets zero uptake.
const journalTaskProtocol = "\n\nIMPORTANT — your context is aggressively evicted (older tool results become stubs). The journal_commit tool is the ONLY way to preserve what you learn. After EVERY bug fix, test run, or rejected hypothesis, call journal_commit before doing anything else. The journal is shown in your system prompt and survives eviction; never re-investigate what it already records."

func hostTools() []bridle.ToolDef {
	obj := func(p string) json.RawMessage { return json.RawMessage(`{"type":"object","properties":` + p + `}`) }
	return []bridle.ToolDef{
		{Name: "list_files", Description: "List all files in the project.", InputSchema: obj(`{}`)},
		{Name: "read_file", Description: "Read a file's full contents.", InputSchema: obj(`{"path":{"type":"string"}},"required":["path"]`)},
		{Name: "write_file", Description: "Overwrite a file with new contents.", InputSchema: obj(`{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]`)},
		{Name: "run_command", Description: "Run a shell command in the project root (e.g. run the tests).", InputSchema: obj(`{"command":{"type":"string"}},"required":["command"]`)},
	}
}

type sink struct{}

func (sink) Emit(bridle.Event) {}

type result struct {
	config        string
	passed        bool
	steps         int
	inTok, outTok int // BACKEND (remote model) tokens only — the paid bill
	wallSec       float64
	// internal AI (local Qwen extractor/judge/distiller) — invisible to the
	// backend token counters; metered separately so cost = backend + internal.
	intCalls    int
	intOutTok   int
	intInTokEst int
	intWallSec  float64
	distCalls   int     // distiller calls specifically (the synchronous tax)
	distWallSec float64
}

func setupSandbox(taskDir string) string {
	tmp, _ := os.MkdirTemp("", "ctxagent-")
	exec.Command("cp", "-r", taskDir+"/.", tmp).Run()
	return tmp
}

// restorePristineTests overwrites the sandbox tests/ from the pristine task dir
// before scoring, so a PASS can only come from src/ actually matching the spec —
// never from the agent editing the tests or golden fixtures (the task forbids
// it, but the harness must not TRUST that; verify the artifact, not the
// narrative). The bugs live only in src/, so the ground-truth tests are safe to
// reset wholesale.
func restorePristineTests(dir, taskDir string) {
	if _, err := os.Stat(filepath.Join(taskDir, "tests")); err != nil {
		return
	}
	os.RemoveAll(filepath.Join(dir, "tests"))
	exec.Command("cp", "-r", filepath.Join(taskDir, "tests"), filepath.Join(dir, "tests")).Run()
}

func score(dir, testCmd string) bool {
	cmd := exec.Command("bash", "-lc", testCmd)
	cmd.Dir = dir
	return cmd.Run() == nil // exit 0 = all pass
}

// seedStore asserts pre-authored facts as VERIFIED (operator-stated,
// performative — the operator handing the agent its briefing rules). This is
// the UPPER-BOUND experiment: "if extraction were perfect, does the rest of
// the chain (store -> inject -> model uses it) deliver?" — it isolates the
// injection/usage links from extraction quality.
func seedStore(st *store.Store, sessionID, path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var seeds []struct {
		Statement string   `json:"statement"`
		Kind      string   `json:"kind"`
		Entities  []string `json:"entities"`
	}
	if err := json.Unmarshal(b, &seeds); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range seeds {
		_, err := st.AssertFact(store.Fact{
			Statement: s.Statement, Kind: store.Kind(s.Kind),
			Trust: store.TrustOperatorStated, Performative: true, Confidence: 1,
			Entities: s.Entities, SessionID: sessionID,
			Provenance: []store.Span{{SessionID: sessionID, Turn: 1, Start: 0, End: len(s.Statement)}},
		})
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func runConfig(name, model string, mapOn, within, state, useCodemap, useJournal, wset bool, keep int, seedFile, taskDir, taskPrompt, testCmd string, maxSteps int) result {
	dir := setupSandbox(taskDir)
	defer os.RemoveAll(dir)

	prov := openai.NewWithBaseURL(env("CTXMAP_API_KEY", "dummy"), env("CTXMAP_BASE_URL", "http://100.92.111.3:4000/v1"))
	h := bridle.NewHarness(prov)

	var eng *memory.Engine
	var ex *extractor.Extractor
	var st *store.Store
	var detach func()
	if mapOn {
		st, _ = store.Open(":memory:")
		rend, _ := render.New(st)
		if seedFile != "" {
			// upper-bound mode: seeded VERIFIED facts, NO extractor at all —
			// isolates injection/usage from extraction quality (and from
			// extractor CPU contention)
			n, err := seedStore(st, name, seedFile)
			if err != nil {
				fmt.Println("seed:", err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "[seed] %d verified facts from %s\n", n, seedFile)
			eng = memory.New(memory.Config{SessionID: name}, st, rend, nil, nil, nil)
		} else {
			var err error
			ex, err = extractor.New(extractor.Config{
				ExtractModelPath: env("CTXMAP_EXTRACT_MODEL", filepath.Join(os.Getenv("HOME"), "models/Qwen3-1.7B-Q8_0.gguf")),
				KindModelPath:    env("CTXMAP_KIND_MODEL", filepath.Join(os.Getenv("HOME"), "models/Qwen3-4B-Q8_0.gguf")),
				Threads:          12,
			})
			if err != nil {
				fmt.Println("extractor:", err)
				os.Exit(1)
			}
			var emb embed.Embedder
			if e, err := embed.NewLlama(env("CTXMAP_EMBED_MODEL", filepath.Join(os.Getenv("HOME"), "models/nomic-embed-text-v1.5.Q8_0.gguf")), 8); err == nil {
				emb = e
				defer e.Close()
			}
			eng = memory.New(memory.Config{SessionID: name}, st, rend, ex, emb, ex)
			d := distill.New(ex, 1500)
			d.SkipTools("read_file", "list_files") // step 2: never distill file/dir reads (lossy on source, ~5% gain, full model call)
			eng.SetDistiller(d)
		}
		if within {
			eng.EnableWithinTurn() // step 3: mine tool results + refresh block each step
		}
		if state {
			eng.EnableWorkingState() // the second memory: deterministic progress tracking
		}
		detach = adapter.Attach(h, eng)
	}

	// context-degradation regime: evict old tool results past the keep window
	// (both map-on and map-off — the cap is the experimental pressure; the map
	// is the intervention)
	if keep > 0 {
		if wset {
			h.RegisterBeforeModelCall((&wsetEvictor{keep: keep}).hook)
		} else {
			ev := &evictor{keep: keep, eng: eng, focus: taskPrompt}
			h.RegisterBeforeModelCall(ev.hook)
		}
	}

	// codemap (arm D): the in-harness symbol server over the sandbox — code_*
	// tools + drift reports on mutation. Independent of ctxmap (deterministic,
	// no local models), so it composes with any config.
	if useCodemap {
		cm := codemap.New(dir, codemap.PyIndexer{})
		detachCM := codemap.Attach(h, cm)
		defer detachCM()
	}

	// decision journal (arm E): agent-authored milestone/rejected-hypothesis
	// checkpoints in an eviction-proof system-prompt block. Independent of
	// ctxmap (no models, no store), composes with any config.
	if useJournal {
		(&journal{}).attach(h)
	}

	t0 := time.Now()
	res := result{config: name}
	// One agentic turn with a high tool-step budget: the model reads, edits,
	// runs the tests, and iterates within bridle's loop until done or capped.
	tr, err := h.RunTurn(context.Background(), bridle.TurnRequest{
		AspectID: "ctxagent", Model: model, MaxSteps: maxSteps,
		UserMessage: taskPrompt, Tools: hostTools(),
	}, &sandboxRunner{dir: dir}, sink{})
	res.wallSec = time.Since(t0).Seconds()
	if err == nil {
		res.steps = tr.StepCount
		res.inTok = int(tr.Usage.InputTokens)
		res.outTok = int(tr.Usage.OutputTokens)
	} else {
		fmt.Printf("  %s: RunTurn error: %v\n", name, err)
	}

	// Tear down the engine and capture the internal-AI meter. eng.Close() drains
	// the async extraction worker, so the meter reflects ALL internal work
	// (extraction included), not just the synchronous distiller calls.
	if mapOn {
		detach()
		eng.Close()
		if ex != nil { // seeded mode runs no extractor
			rep := ex.Report()
			tot := rep.Total()
			res.intCalls, res.intOutTok, res.intInTokEst = tot.Calls, tot.OutTokens, tot.InTokensEst
			res.intWallSec = tot.Wall.Seconds()
			res.distCalls, res.distWallSec = rep.Distill.Calls, rep.Distill.Wall.Seconds()
			ex.Close()
		}
		// dump the store: the extraction-quality evidence. "Did the map hold the
		// load-bearing facts?" is only answerable by reading what was mined.
		if facts, err := st.All(); err == nil {
			fmt.Fprintf(os.Stderr, "==== %s: store dump (%d facts) ====\n", name, len(facts))
			for _, f := range facts {
				fmt.Fprintf(os.Stderr, "  [%s %s %s] %s\n", f.Kind, f.Status, f.Trust, f.Statement)
			}
			fmt.Fprintf(os.Stderr, "==== end store dump ====\n")
		}
	}

	if err != nil {
		return res
	}
	restorePristineTests(dir, taskDir) // step 4: score only src/, never trust the agent left tests/ intact
	res.passed = score(dir, testCmd)
	return res
}

func main() {
	taskDir := flag.String("task", "tasks/kvstore", "task directory")
	reps := flag.Int("reps", 1, "reps per config")
	maxSteps := flag.Int("steps", 30, "max tool-call rounds")
	small := flag.String("small", "ornith", "small model")
	big := flag.String("big", "deepseek-v4-pro", "big model")
	only := flag.String("only", "", "run only this config (small-on|small-off|big-on|big-off)")
	within := flag.Bool("within", false, "within-turn mode: ingest tool results + refresh block each step (map-on configs)")
	keep := flag.Int("keep", 0, "evict tool results older than the last N (0 = no eviction) — forces the context-degradation regime")
	seed := flag.String("seed", "", "seed file of verified facts (upper-bound mode: no extractor; isolates injection from extraction)")
	state := flag.Bool("state", false, "working-state block: deterministic progress tracking (files edited, last test result, recent steps)")
	useCodemap := flag.Bool("codemap", false, "attach the codemap symbol server (code_* tools + drift reports) over the sandbox")
	useJournal := flag.Bool("journal", false, "decision journal (arm E): journal_commit tool + eviction-proof system-prompt block")
	wset := flag.Bool("wset", false, "working-set retention (arm F): latest read of each file never evicted; command outputs keep last N")
	flag.Parse()

	prompt, _ := os.ReadFile(filepath.Join(*taskDir, "README-task.md"))
	// Per-task test command: <taskDir>/test-cmd if present, else the kvstore default.
	testCmd := "python3 tests/test_kv.py"
	if b, err := os.ReadFile(filepath.Join(*taskDir, "test-cmd")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			testCmd = s
		}
	}
	taskPrompt := string(prompt) + "\n\nUse the tools to explore, fix the code, and run the tests until they pass. When all tests pass, stop."
	if *useJournal {
		taskPrompt += journalTaskProtocol
	}

	configs := []struct {
		name, model string
		mapOn       bool
	}{
		{"small-on", *small, true},
		{"small-off", *small, false},
		{"big-on", *big, true},
		{"big-off", *big, false},
	}
	fmt.Printf("task=%s reps=%d steps=%d small=%s big=%s\n", *taskDir, *reps, *maxSteps, *small, *big)
	for _, c := range configs {
		if *only != "" && c.name != *only {
			continue
		}
		for r := 0; r < *reps; r++ {
			res := runConfig(c.name, c.model, c.mapOn, *within, *state, *useCodemap, *useJournal, *wset, *keep, *seed, *taskDir, taskPrompt, testCmd, *maxSteps)
			status := "FAIL"
			if res.passed {
				status = "PASS"
			}
			// backend = paid remote tokens; internal = local Qwen (extractor/
			// judge/distiller), invisible to the backend counter. Report both so
			// cost is total, not just the remote bill.
			fmt.Printf("[%s r%d] %s  steps=%d backend[in=%d out=%d] internal[calls=%d out=%d inEst=%d %.0fs; distill=%dcalls/%.0fs] wall=%.0fs\n",
				res.config, r, status, res.steps, res.inTok, res.outTok,
				res.intCalls, res.intOutTok, res.intInTokEst, res.intWallSec,
				res.distCalls, res.distWallSec, res.wallSec)
			if err := taskIntegrityCheck(*taskDir); err != nil {
				fmt.Println("ABORT:", err)
				os.Exit(2)
			}
		}
	}
}
