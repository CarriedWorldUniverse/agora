//go:build !windows

// This test probes subprocess liveness with signal 0, which Windows does
// not support (os.Process.Signal on Windows can only deliver Kill) — so
// the PID-liveness verification is unix-only. The teardown path it guards
// (Source.Close's Client-then-Cancel ordering) is platform-independent
// and still covered on Windows by the fake-based turnengine tests.

package mcp

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

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
