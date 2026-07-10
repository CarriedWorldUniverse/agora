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
	"time"

	bridle "github.com/CarriedWorldUniverse/bridle"
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

func (r *sandboxRunner) Run(_ context.Context, call bridle.ToolCall) (json.RawMessage, error) {
	var a struct {
		Path, Content, Command string
	}
	json.Unmarshal(call.Args, &a)
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
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-lc", a.Command)
		cmd.Dir = r.dir
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
	config          string
	passed          bool
	steps           int
	inTok, outTok   int
	toolResultChars int // raw chars the backend WOULD have seen without distilling (map-off) / did (map-off)
	wallSec         float64
}

func setupSandbox(taskDir string) string {
	tmp, _ := os.MkdirTemp("", "ctxagent-")
	exec.Command("cp", "-r", taskDir+"/.", tmp).Run()
	return tmp
}

func score(dir, testCmd string) bool {
	cmd := exec.Command("bash", "-lc", testCmd)
	cmd.Dir = dir
	return cmd.Run() == nil // exit 0 = all pass
}

func runConfig(name, model string, mapOn bool, taskDir, taskPrompt, testCmd string, maxSteps int) result {
	dir := setupSandbox(taskDir)
	defer os.RemoveAll(dir)

	prov := openai.NewWithBaseURL(env("CTXMAP_API_KEY", "dummy"), env("CTXMAP_BASE_URL", "http://100.92.111.3:4000/v1"))
	h := bridle.NewHarness(prov)

	var eng *memory.Engine
	if mapOn {
		ex, err := extractor.New(extractor.Config{
			ExtractModelPath: env("CTXMAP_EXTRACT_MODEL", filepath.Join(os.Getenv("HOME"), "models/Qwen3-1.7B-Q8_0.gguf")),
			KindModelPath:    env("CTXMAP_KIND_MODEL", filepath.Join(os.Getenv("HOME"), "models/Qwen3-4B-Q8_0.gguf")),
			Threads:          12,
		})
		if err != nil {
			fmt.Println("extractor:", err)
			os.Exit(1)
		}
		defer ex.Close()
		st, _ := store.Open(":memory:")
		rend, _ := render.New(st)
		var emb embed.Embedder
		if e, err := embed.NewLlama(env("CTXMAP_EMBED_MODEL", filepath.Join(os.Getenv("HOME"), "models/nomic-embed-text-v1.5.Q8_0.gguf")), 8); err == nil {
			emb = e
			defer e.Close()
		}
		eng = memory.New(memory.Config{SessionID: name}, st, rend, ex, emb, ex)
		eng.SetDistiller(distill.New(ex, 1500))
		defer eng.Close()
		detach := adapter.Attach(h, eng)
		defer detach()
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
	if err != nil {
		fmt.Printf("  %s: RunTurn error: %v\n", name, err)
		return res
	}
	res.steps = tr.StepCount
	res.inTok = int(tr.Usage.InputTokens)
	res.outTok = int(tr.Usage.OutputTokens)
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
	flag.Parse()

	prompt, _ := os.ReadFile(filepath.Join(*taskDir, "README-task.md"))
	testCmd := "python3 tests/test_kv.py"
	taskPrompt := string(prompt) + "\n\nUse the tools to explore, fix the code, and run the tests until they pass. When all tests pass, stop."

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
			res := runConfig(c.name, c.model, c.mapOn, *taskDir, taskPrompt, testCmd, *maxSteps)
			status := "FAIL"
			if res.passed {
				status = "PASS"
			}
			fmt.Printf("[%s r%d] %s  steps=%d in=%d out=%d %.0fs\n",
				res.config, r, status, res.steps, res.inTok, res.outTok, res.wallSec)
		}
	}
}
