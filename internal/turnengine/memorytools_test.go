package turnengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// isolateMemoryHome points HOME at a fresh temp dir so defaultMemoryDir()
// (and thus the memory.* tool family/index injection) never touches the
// real operator machine's ~/.agora/memory — hermeticity, same rationale as
// isolateSkillsEnv. Returns the resolved memory store dir
// (~/.agora/memory/default) for assertions.
func isolateMemoryHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Windows resolves os.UserHomeDir from USERPROFILE, not HOME —
	// without this the isolation silently fails on Windows CI.
	t.Setenv("USERPROFILE", home)
	return filepath.Join(home, ".agora", "memory", "default")
}

func memoryWriteCall(id, name, fmName, fmDesc, fmType, body string) bridle.ToolInvocation {
	args, _ := json.Marshal(map[string]any{
		"name": name,
		"frontmatter": map[string]string{
			"name":        fmName,
			"description": fmDesc,
			"type":        fmType,
		},
		"body": body,
	})
	return bridle.ToolInvocation{ID: id, Name: contracts.ToolMemoryWrite, Args: args}
}

// TestManager_ToolCall_MemoryWriteExecutesViaSurface: a memory.write tool
// call, dispatched through the real Surface/MemoryFamily (not a stub),
// persists dir/<name>.md AND rebuilds MEMORY.md — read back from disk here,
// proving the wiring actually reaches internal/memory.Store, not just that
// the call returns success.
func TestManager_ToolCall_MemoryWriteExecutesViaSurface(t *testing.T) {
	memDir := isolateMemoryHome(t)
	roots := managerTestRoots(t)

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			memoryWriteCall("1", "op-prefs", "Operator preferences", "how the operator likes things done", "user", "prefers terse commit messages"),
		}},
		fake.Step{Text: "done"},
	)
	// memory.write classifies as KindPatch (mirrors write_file) — allow it
	// so this test proves DISPATCH, not approval semantics (which has its
	// own dedicated coverage below).
	policy := allowAllPolicy()
	m := NewManager("th_mem_write", provider, WithRoots(roots), WithPolicy(policy), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "remember my prefs"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	entryData, err := os.ReadFile(filepath.Join(memDir, "op-prefs.md"))
	if err != nil {
		t.Fatalf("read op-prefs.md: %v (memory.write did not persist to disk)", err)
	}
	if !strings.Contains(string(entryData), "prefers terse commit messages") {
		t.Fatalf("op-prefs.md content = %q; want the saved body", entryData)
	}

	idxData, err := os.ReadFile(filepath.Join(memDir, "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v (memory.write did not rebuild the index)", err)
	}
	if !strings.Contains(string(idxData), "op-prefs.md") {
		t.Fatalf("MEMORY.md content = %q; want an index line for op-prefs", idxData)
	}
}

// TestManager_ToolCall_MemoryReadReturnsContentAsToolResult: memory.read
// dispatched through the real Surface returns the previously-written
// content in the tool_result message the provider sees next round.
func TestManager_ToolCall_MemoryReadReturnsContentAsToolResult(t *testing.T) {
	memDir := isolateMemoryHome(t)
	roots := managerTestRoots(t)
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "---\nname: Operator preferences\ndescription: hook\ntype: user\n---\n\nprefers terse commit messages\n"
	if err := os.WriteFile(filepath.Join(memDir, "op-prefs.md"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			{ID: "1", Name: contracts.ToolMemoryRead, Args: json.RawMessage(`{"name":"op-prefs"}`)},
		}},
		fake.Step{Text: "done"},
	)
	// memory.read classifies as KindRead (mirrors read_file, NEX-782), which
	// defaultPolicy() already auto-allows — no WithPolicy override needed.
	m := NewManager("th_mem_read", provider, WithRoots(roots), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "what are my prefs"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	toolMsg := lastToolResultMessage(t, provider.LastRequest())
	if !strings.Contains(toolMsg.Content, "prefers terse commit messages") {
		t.Fatalf("tool_result content = %q; want the saved memory body", toolMsg.Content)
	}
}

// TestManager_Approval_MemoryWriteClassifiesAsPatchAndDenyBlocksIt proves
// memory.write is gated as a MUTATING kind (KindPatch, mirroring
// write_file) under the Manager's default policy (KindPatch=Prompt): the
// call must ask for approval, and a deny must leave no file on disk —
// exactly TestManager_Approval_AskDenyIsToolResultNotAbort's write_file
// case, retargeted at memory.write.
func TestManager_Approval_MemoryWriteClassifiesAsPatchAndDenyBlocksIt(t *testing.T) {
	memDir := isolateMemoryHome(t)
	roots := managerTestRoots(t)

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{
			memoryWriteCall("1", "op-prefs", "Operator preferences", "hook", "user", "should never land on disk"),
		}},
		fake.Step{Text: "ok, I won't"},
	)
	// promptAllPolicy: exercises the deny path itself (memory.write shares
	// KindPatch with write_file — sandbox-first's defaultPolicy now auto-
	// allows Patch by default, same as a regular write, since MemoryFamily's
	// own validateSlug containment is memory.write's safety net just as
	// roots-containment is write_file's; this test still proves a policy
	// that DOES prompt correctly blocks a denied memory write).
	m := NewManager("th_mem_deny", provider, WithRoots(roots), WithPolicy(promptAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "remember this"}

	req := recvApprovalRequested(t, out, testTimeout)
	var ar contracts.ApprovalRequest
	if err := json.Unmarshal(req.Payload, &ar); err != nil {
		t.Fatalf("decode approval.requested payload: %v", err)
	}
	if ar.Kind != contracts.KindPatch {
		t.Fatalf("approval kind = %q; want patch (memory.write mirrors write_file's mutating classification)", ar.Kind)
	}
	var pp toolrunner.PatchPayload
	if pb, err := json.Marshal(ar.Payload); err == nil {
		_ = json.Unmarshal(pb, &pp)
	}
	if pp.Path != "op-prefs.md" {
		t.Fatalf("approval payload path = %q; want op-prefs.md (payload=%+v)", pp.Path, ar.Payload)
	}

	in <- contracts.Input{Type: contracts.InApprovalResponse, ID: ar.ID, Decision: contracts.DecisionDeny, Message: "not right now"}

	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed after deny")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}

	if _, err := os.Stat(filepath.Join(memDir, "op-prefs.md")); !os.IsNotExist(err) {
		t.Fatalf("op-prefs.md should not exist on disk after a denial, stat err = %v", err)
	}
}
