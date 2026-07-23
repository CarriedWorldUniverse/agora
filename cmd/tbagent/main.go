// Command tbagent is the headless benchmark agent for Terminal-Bench 2.0
// (Harbor's BaseInstalledAgent pattern): it is installed into a task
// container, receives one task instruction, drives one turnengine.Manager
// turn to completion against an OpenAI-compatible backend, and exits.
//
// It exists to put agora's context-curation ContextManager (internal/ctxmgr,
// the bridle/wset v2 algorithm) on a public benchmark with a same-model
// ablation: -curation=true vs -curation=false is the ONLY difference between
// the bridle-wset and bridle-bare leaderboard cells.
//
// Construction mirrors internal/subagent/enginerunner (the tested one-shot
// headless turn driver): one Manager, one user_message, drain events until
// the terminal turn event, InEnd, wait for Run to return. Differences from
// enginerunner, each deliberate:
//   - approval policy is all-auto except Question/Plan (denied): there is no
//     operator; a benchmark agent must never block on an ask.
//   - every event is journaled to a trajectory JSONL and closing usage to a
//     metrics JSON — the Harbor wrapper parses these into AgentContext.
//   - the agent process exits 0 whether or not the model "solved" the task:
//     Terminal-Bench's verifier judges the final container state, never the
//     agent's own account of itself (the observable-criteria rule).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
	openai "github.com/CarriedWorldUniverse/bridle/provider/openai"
)

// benchSystemPrompt is appended to the Manager's system prompt. It states
// the operating regime (headless, verifier-scored) — not task strategy.
const benchSystemPrompt = `You are an autonomous agent running headlessly inside a Linux container to complete one concrete task. There is no human available: never ask questions, never wait for confirmation — decide and act. The task is scored by automated tests on the final state of the container after you finish, so make the requested end state true on disk, then stop. Verify your work with the tools before finishing.`

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "tbagent: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		instrFile = flag.String("instruction-file", "", "path to a file holding the task instruction")
		instrB64  = flag.String("instruction-b64", "", "base64-encoded task instruction (overrides -instruction-file)")
		workdir   = flag.String("workdir", "/", "root the fs tools operate under (the container is the sandbox)")
		outDir    = flag.String("out", "/tbagent-logs", "directory for trajectory.jsonl + metrics.json")
		maxSteps  = flag.Int("max-steps", 400, "tool-loop step cap for the single turn")
		curation  = flag.Bool("curation", envBool("TB_CURATION", true), "enable the ctxmgr context-curation ContextManager (the ablation switch)")
		timeout   = flag.Duration("timeout", envDuration("TB_TIMEOUT", 0), "optional wall-clock cap; 0 = rely on the harness's task timeout")
	)
	flag.Parse()

	model := os.Getenv("TB_MODEL")
	baseURL := os.Getenv("TB_BASE_URL")
	apiKey := os.Getenv("TB_API_KEY")
	if apiKey == "" {
		apiKey = "dummy" // buildProvider's convention for keyless local gateways
	}
	if model == "" || baseURL == "" {
		return fmt.Errorf("TB_MODEL and TB_BASE_URL are required")
	}

	instruction, err := loadInstruction(*instrB64, *instrFile)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fmt.Errorf("out dir: %w", err)
	}
	traj, err := os.Create(*outDir + "/trajectory.jsonl")
	if err != nil {
		return fmt.Errorf("trajectory: %w", err)
	}
	defer traj.Close()
	journal := json.NewEncoder(traj)

	roots, err := toolrunner.NewRoots(*workdir)
	if err != nil {
		return fmt.Errorf("roots: %w", err)
	}

	// No operator: everything the model can do runs unattended; the two ask-
	// shaped kinds are denied outright so a stray question() tool call gets a
	// refusal result instead of a hang.
	policy := contracts.PolicySet{
		contracts.KindExec:       contracts.PolicyAuto,
		contracts.KindPatch:      contracts.PolicyAuto,
		contracts.KindEscalation: contracts.PolicyAuto,
		contracts.KindMCPTool:    contracts.PolicyAuto,
		contracts.KindRead:       contracts.PolicyAuto,
		contracts.KindQuestion:   contracts.PolicyDeny,
		contracts.KindPlan:       contracts.PolicyDeny,
	}

	provider := openai.NewWithBaseURL(apiKey, baseURL)
	mgr := turnengine.NewManager("tbagent", provider,
		turnengine.WithModel(model),
		turnengine.WithMaxSteps(*maxSteps),
		turnengine.WithPolicy(policy),
		turnengine.WithRoots(roots),
		// Minimal surface: fs + exec only. No memory family (dev-identity
		// store, meaningless in a container — and its dotted tool names are
		// rejected by DeepSeek's tool-name pattern), no planning family
		// (question/plan are operator-facing; the policy denies them anyway).
		turnengine.WithToolFamilies(
			toolrunner.NewFSFamily(roots),
			toolrunner.NewExecFamily(roots),
		),
		turnengine.WithAppendSystemPrompt(benchSystemPrompt),
		turnengine.WithContextCuration(*curation),
		// The working-state context engine is a separate mechanism from the
		// curation under test; off in BOTH cells so the ablation isolates
		// ctxmgr alone.
		turnengine.WithContextEngine(false),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if *timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sig; cancel() }()

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 256)
	runDone := make(chan error, 1)
	go func() { runDone <- mgr.Run(ctx, in, out) }()

	select {
	case in <- contracts.Input{Type: contracts.InUserMessage, Text: instruction}:
	case <-ctx.Done():
		close(in)
		<-runDone
		return ctx.Err()
	}

	var (
		usage    contracts.Usage
		outcome  string
		errMsg   string
		endSent  bool
		started  = time.Now()
		numSteps int
	)

drain:
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				break drain
			}
			_ = journal.Encode(ev) // journaling must never abort the run
			switch ev.Type {
			case contracts.EvItemCompleted:
				if ev.Item != nil && (ev.Item.Type == contracts.ItemCommandExecution ||
					ev.Item.Type == contracts.ItemFileChange ||
					ev.Item.Type == contracts.ItemMCPToolCall) {
					numSteps++
				}
			case contracts.EvError:
				var p struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(ev.Payload, &p) == nil && p.Message != "" {
					errMsg = p.Message
				}
			case contracts.EvTurnCompleted:
				outcome = "completed"
				var p struct {
					Usage *contracts.Usage `json:"usage"`
				}
				if json.Unmarshal(ev.Payload, &p) == nil && p.Usage != nil {
					usage = *p.Usage
				}
			case contracts.EvTurnFailed:
				outcome = "failed"
			}
			if outcome != "" && !endSent {
				endSent = true
				select {
				case in <- contracts.Input{Type: contracts.InEnd}:
				case <-ctx.Done():
				}
			}
		case <-ctx.Done():
			break drain
		}
	}
	<-runDone

	metrics := map[string]any{
		"outcome":       outcome,
		"error":         errMsg,
		"model":         model,
		"curation":      *curation,
		"tool_steps":    numSteps,
		"wall_seconds":  time.Since(started).Seconds(),
		"input_tokens":  usage.Input,
		"output_tokens": usage.Output,
		"cached_tokens": usage.Cached,
		"cost_usd":      usage.Cost,
	}
	mb, _ := json.MarshalIndent(metrics, "", "  ")
	if werr := os.WriteFile(*outDir+"/metrics.json", mb, 0o644); werr != nil {
		fmt.Fprintf(os.Stderr, "tbagent: metrics write: %v\n", werr)
	}
	fmt.Printf("tbagent: outcome=%s steps=%d in=%d out=%d cached=%d\n",
		outcome, numSteps, usage.Input, usage.Output, usage.Cached)

	// Exit 0 on any finished turn — the verifier owns pass/fail. Nonzero only
	// for harness-level breakage (no terminal event / interrupted).
	if outcome == "" {
		if ctx.Err() != nil {
			return fmt.Errorf("turn interrupted: %v (error: %s)", ctx.Err(), errMsg)
		}
		return fmt.Errorf("turn ended with no terminal event (error: %s)", errMsg)
	}
	return nil
}

func loadInstruction(b64, file string) (string, error) {
	if b64 != "" {
		b, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", fmt.Errorf("instruction-b64: %w", err)
		}
		return string(b), nil
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("instruction-file: %w", err)
		}
		return string(b), nil
	}
	return "", fmt.Errorf("one of -instruction-b64 or -instruction-file is required")
}

func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
