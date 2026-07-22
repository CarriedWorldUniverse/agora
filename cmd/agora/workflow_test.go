package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

func readTestdataFile(t *testing.T, name string) string {
	t.Helper()
	// Copy the fixture into the test's own temp dir so run-dir/thread-store
	// isolation (journalDir, below) has no bearing on where the SCRIPT
	// argument lives — cmd/agora/testdata is read-only source, never
	// written to.
	src := filepath.Join("testdata", name)
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatalf("stage %s: %v", dst, err)
	}
	return dst
}

// TestWorkflowRun_E2E_OneAgent drives `agora workflow run` end-to-end
// against a one-ctx.agent-call fixture with a scripted fake provider (no
// model, no billing — bridle/fake, the same seam the turn-engine's own
// tests use): exit 0, stdout carries the workflow's JSON return, the
// journal file exists with run entries, and the spawned child agent's
// thread persisted in the store.
func TestWorkflowRun_E2E_OneAgent(t *testing.T) {
	script := readTestdataFile(t, "one_agent.star")
	journalDir := filepath.Join(t.TempDir(), "workflow-runs")
	provider := fake.NewProvider(fake.Step{Text: "hi there", Usage: bridle.Usage{InputTokens: 1, OutputTokens: 1}})

	var stdout, stderr bytes.Buffer
	code := runWorkflowRun([]string{"-args", `{"who":"world"}`, "-journal", journalDir, script}, &stdout, &stderr, provider)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}

	var result map[string]any
	stdoutLine := strings.TrimSpace(stdout.String())
	if err := json.Unmarshal([]byte(stdoutLine), &result); err != nil {
		t.Fatalf("decode stdout JSON %q: %v", stdoutLine, err)
	}
	if result["greeting"] != "hi there" {
		t.Fatalf("result = %+v, want greeting=%q", result, "hi there")
	}

	// The journal file exists with run entries.
	entries, err := os.ReadDir(journalDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir(%s) = %v, %v; want exactly one run dir", journalDir, entries, err)
	}
	runID := entries[0].Name()
	journalPath := filepath.Join(journalDir, runID, "journal.jsonl")
	jb, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal %s: %v", journalPath, err)
	}
	lines := strings.Split(strings.TrimSpace(string(jb)), "\n")
	if len(lines) < 2 {
		t.Fatalf("journal has %d lines, want at least 2 (run_start + one agent entry): %s", len(lines), jb)
	}
	sawAgentEntry := false
	for _, line := range lines {
		var e struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("decode journal line %q: %v", line, err)
		}
		if e.Kind == "agent" {
			sawAgentEntry = true
		}
	}
	if !sawAgentEntry {
		t.Fatalf("journal has no agent-kind entry: %s", jb)
	}

	// run-meta.json reflects the completed run (feeds `list`).
	meta, err := readRunMeta(filepath.Join(journalDir, runID))
	if err != nil {
		t.Fatalf("readRunMeta: %v", err)
	}
	if meta.Status != "completed" || meta.Name != "one-agent-demo" {
		t.Fatalf("run-meta = %+v; want status=completed name=one-agent-demo", meta)
	}

	// The spawned child agent's thread persisted (spec §2: "Every
	// ctx.agent() IS a real subagent ... its transcript in the thread
	// store"). Reopen the store the run wrote to and expect the run's own
	// thread PLUS at least one child.
	store, err := openWorkflowStore(journalDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() {
		if c, ok := store.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}()
	threads, err := store.List(contracts.ListFilter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(threads) < 2 {
		t.Fatalf("persisted threads = %d, want at least 2 (run thread + spawned child): %+v", len(threads), threads)
	}
	var sawChild bool
	for _, tm := range threads {
		if tm.ParentThread == "wf-"+runID {
			sawChild = true
		}
	}
	if !sawChild {
		t.Fatalf("no persisted thread has ParentThread=%q: %+v", "wf-"+runID, threads)
	}
}

// TestWorkflowRun_Resume drives a 2-step fixture where the FIRST provider
// only has enough scripted steps to complete stage 1 — stage 2's live
// invocation then fails, mirroring a crash/kill mid-run (internal/workflow's
// own resume tests, engine_test.go, exercise the same "edit/crash the tail"
// property directly against Run; this reproduces it at the CLI/journal-file
// layer). `-resume <run-id>` with a SECOND provider that has exactly one
// scripted step must then complete using stage 1's CACHED result: if stage 1
// were incorrectly re-invoked live, it would consume the resume provider's
// only step, stage 2 would then find none left, and the run would fail
// instead of completing.
func TestWorkflowRun_Resume(t *testing.T) {
	script := readTestdataFile(t, "two_agent.star")
	journalDir := filepath.Join(t.TempDir(), "workflow-runs")

	// Stage 1 succeeds; stage 2 fails EXPLICITLY (Err step). The original
	// version relied on provider exhaustion erroring — enginerunner (the
	// production AgentRunner this CLI now shares with interactive agent()
	// spawns) correctly treats an empty reply as success, so the failure
	// must be scripted, not implied.
	provider1 := fake.NewProvider(fake.Step{Text: "stage1-out"}, fake.Step{Err: errors.New("stage 2 boom")})
	var stdout1, stderr1 bytes.Buffer
	code := runWorkflowRun([]string{"-args", `{"seed":"x"}`, "-journal", journalDir, script}, &stdout1, &stderr1, provider1)
	if code != 1 {
		t.Fatalf("first attempt exit code = %d, want 1 (stage 2 should fail live); stdout=%s stderr=%s", code, stdout1.String(), stderr1.String())
	}

	entries, err := os.ReadDir(journalDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir(%s) = %v, %v; want exactly one run dir", journalDir, entries, err)
	}
	runID := entries[0].Name()

	meta, err := readRunMeta(filepath.Join(journalDir, runID))
	if err != nil {
		t.Fatalf("readRunMeta: %v", err)
	}
	if meta.Status != "errored" {
		t.Fatalf("run-meta.status = %q after the killed attempt, want errored", meta.Status)
	}

	provider2 := fake.NewProvider(fake.Step{Text: "stage2-out"})
	var stdout2, stderr2 bytes.Buffer
	code = runWorkflowRun([]string{"-resume", runID, "-journal", journalDir}, &stdout2, &stderr2, provider2)
	if code != 0 {
		t.Fatalf("resume exit code = %d, want 0; stdout=%s stderr=%s", code, stdout2.String(), stderr2.String())
	}
	if provider2.StepsRemaining() != 0 {
		t.Fatalf("resume provider's step was not consumed by stage 2 (StepsRemaining=%d) — stage 1 must have replayed from cache, not re-invoked", provider2.StepsRemaining())
	}

	var result map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout2.Bytes()), &result); err != nil {
		t.Fatalf("decode resumed stdout JSON %q: %v", stdout2.String(), err)
	}
	if result["a"] != "stage1-out" {
		t.Fatalf(`result["a"] = %v, want the ORIGINAL cached "stage1-out" (proves stage 1 replayed)`, result["a"])
	}
	if result["b"] != "stage2-out" {
		t.Fatalf(`result["b"] = %v, want "stage2-out" (stage 2 ran live on resume)`, result["b"])
	}

	meta, err = readRunMeta(filepath.Join(journalDir, runID))
	if err != nil {
		t.Fatalf("readRunMeta after resume: %v", err)
	}
	if meta.Status != "completed" {
		t.Fatalf("run-meta.status after resume = %q, want completed", meta.Status)
	}
}

// TestWorkflowList_ShowsCompletedRun exercises `agora workflow list` after
// a completed run: the table carries the run id, workflow name, and status.
func TestWorkflowList_ShowsCompletedRun(t *testing.T) {
	script := readTestdataFile(t, "one_agent.star")
	journalDir := filepath.Join(t.TempDir(), "workflow-runs")
	provider := fake.NewProvider(fake.Step{Text: "hi there"})

	var stdout, stderr bytes.Buffer
	if code := runWorkflowRun([]string{"-args", `{"who":"world"}`, "-journal", journalDir, script}, &stdout, &stderr, provider); code != 0 {
		t.Fatalf("run exit code = %d, want 0; stderr=%s", code, stderr.String())
	}

	var listOut bytes.Buffer
	code := runWorkflowList([]string{"-journal", journalDir}, &listOut)
	if code != 0 {
		t.Fatalf("list exit code = %d, want 0", code)
	}
	out := listOut.String()
	if !strings.Contains(out, "one-agent-demo") {
		t.Fatalf("list output missing workflow name %q: %s", "one-agent-demo", out)
	}
	if !strings.Contains(out, "completed") {
		t.Fatalf("list output missing status %q: %s", "completed", out)
	}
}

// TestWorkflowRun_ParseError feeds an invalid .star file straight through
// `agora workflow run`: a clean error on stderr, exit 1, no panic (Run
// returning normally with code==1 is itself the no-panic assertion — a
// panic escaping this call would crash the whole test binary, not just
// fail this test).
func TestWorkflowRun_ParseError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "broken.star")
	if err := os.WriteFile(script, []byte("def main(ctx, args):\n    this is not valid starlark ("), 0o644); err != nil {
		t.Fatalf("write broken fixture: %v", err)
	}
	journalDir := filepath.Join(t.TempDir(), "workflow-runs")

	var stdout, stderr bytes.Buffer
	code := runWorkflowRun([]string{"-journal", journalDir, script}, &stdout, &stderr, fake.NewProvider())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.Len() == 0 {
		t.Fatalf("expected a stderr message on parse error, got none")
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no stdout on parse error, got %q", stdout.String())
	}
}
