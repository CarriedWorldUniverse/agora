package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func skipNoPython(t *testing.T) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not in PATH")
	}
	return py
}

// TestStdioClient_Handshake_ListAndCall drives the REAL StdioClient against
// the fake python MCP server: initialize handshake, tools/list, tools/call.
func TestStdioClient_Handshake_ListAndCall(t *testing.T) {
	py := skipNoPython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := newStdioClient(ctx, ServerConfig{
		Transport: TransportStdio,
		Command:   py,
		Args:      []string{"testdata/fakemcp.py"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("newStdioClient: %v", err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v; want one 'echo'", tools)
	}

	text, isErr, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || isErr {
		t.Fatalf("CallTool: text=%q isErr=%v err=%v", text, isErr, err)
	}
	if text != "echo:hi" {
		t.Fatalf("CallTool text = %q, want echo:hi", text)
	}
}

// TestSource_OverFakeServer: NewSource end-to-end — qualified names and
// routing back to the raw tool through the manager/client.
func TestSource_OverFakeServer(t *testing.T) {
	py := skipNoPython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	src := NewSource([]ServerConfig{{
		Name: "fake", Transport: TransportStdio, Command: py,
		Args: []string{"testdata/fakemcp.py"}, Enabled: true,
	}})
	defer src.Close()

	tools, err := src.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "mcp__fake__echo" {
		t.Fatalf("tools = %+v; want mcp__fake__echo", tools)
	}
	res, err := src.Call(ctx, "mcp__fake__echo", json.RawMessage(`{"text":"yo"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError || res.Content != "echo:yo" {
		t.Fatalf("Call result = %+v, want echo:yo", res)
	}
	// Unknown qualified name -> clean IsError, not a Go error.
	bad, err := src.Call(ctx, "mcp__fake__nope", nil)
	if err != nil || !bad.IsError {
		t.Fatalf("unknown-tool call = (%+v, %v); want a clean IsError result", bad, err)
	}
}

// alive reports whether pid still exists (signal 0: no actual signal sent,
// just existence/permission checked — the standard liveness probe).
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// TestSource_Close_ActuallyKillsTheSubprocess: adversarial review of
// PR #94, finding 3 — Close() must reach an already-READY client and stop
// its real subprocess, not just cancel a connect future (Cancel deletes
// the future unconditionally; calling it before Client(name) silently
// skips every server that already finished connecting — the bug the
// fix's Client-then-Cancel ordering closes). Verified by PID liveness,
// not just "Close() didn't error".
func TestSource_Close_ActuallyKillsTheSubprocess(t *testing.T) {
	py := skipNoPython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	src := NewSource([]ServerConfig{{
		Name: "fake", Transport: TransportStdio, Command: py,
		Args: []string{"testdata/fakemcp.py"}, Enabled: true,
	}})
	if _, err := src.Tools(ctx); err != nil {
		t.Fatalf("Tools (forces the server to start): %v", err)
	}
	client, ok := src.mgr.Client("fake")
	if !ok {
		t.Fatal("server never reached ready")
	}
	sc, ok := client.(*StdioClient)
	if !ok {
		t.Fatalf("client is %T, want *StdioClient", client)
	}
	pid := sc.cmd.Process.Pid
	if !alive(pid) {
		t.Fatalf("subprocess pid %d not alive right after startup", pid)
	}

	src.Close()

	if alive(pid) {
		t.Fatalf("subprocess pid %d still alive after Close() — the fix did not actually kill it", pid)
	}
}
