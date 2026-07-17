package toolrunner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// fakeMCPSource is an in-process MCPSource fake — no real MCP machinery,
// per the brief ("do NOT import heavyweight MCP machinery into your
// tests").
type fakeMCPSource struct {
	tools   []contracts.ToolSpec
	calls   []Call
	listErr error
}

func (f *fakeMCPSource) Tools(ctx context.Context) ([]contracts.ToolSpec, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *fakeMCPSource) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	f.calls = append(f.calls, Call{Name: name, Args: args})
	return Result{Content: "mcp:" + name}, nil
}

func TestSurfaceSpecsMergesFamiliesAndMCP(t *testing.T) {
	roots := newTestRoots(t)
	fs := NewFSFamily(roots)
	ex := NewExecFamily(roots)
	mcp := &fakeMCPSource{tools: []contracts.ToolSpec{{Name: "mcp__github__search", Description: "search"}}}

	s := NewSurface(mcp, fs, ex)
	specs, err := s.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs: %v", err)
	}
	want := len(fs.Specs()) + len(ex.Specs()) + 1
	if len(specs) != want {
		t.Fatalf("got %d specs, want %d", len(specs), want)
	}

	found := false
	for _, sp := range specs {
		if sp.Name == "mcp__github__search" {
			found = true
		}
	}
	if !found {
		t.Fatal("mcp tool not present in merged specs")
	}
}

func TestSurfaceSpecsNilMCP(t *testing.T) {
	roots := newTestRoots(t)
	fs := NewFSFamily(roots)
	s := NewSurface(nil, fs)
	specs, err := s.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs: %v", err)
	}
	if len(specs) != len(fs.Specs()) {
		t.Fatalf("got %d specs, want %d", len(specs), len(fs.Specs()))
	}
}

func TestSurfaceSpecsMCPListError(t *testing.T) {
	roots := newTestRoots(t)
	fs := NewFSFamily(roots)
	mcp := &fakeMCPSource{listErr: errors.New("boom")}
	s := NewSurface(mcp, fs)
	if _, err := s.Specs(context.Background()); err == nil {
		t.Fatal("expected error from mcp listing failure")
	}
}

func TestSurfaceExecuteRoutesNative(t *testing.T) {
	roots := newTestRoots(t)
	fs := NewFSFamily(roots)
	ex := NewExecFamily(roots)
	mcp := &fakeMCPSource{}
	s := NewSurface(mcp, fs, ex)

	// Route to a cross-platform native tool (list_dir on the working root):
	// this proves native-vs-mcp dispatch on every OS. exec routing (run_command
	// → /bin/sh) is unix-only and covered in exec_test.go (//go:build !windows).
	res, err := s.Execute(context.Background(), Call{Name: ToolListDir, Args: []byte(`{"path":"."}`)})
	if err != nil || res.IsError {
		t.Fatalf("Execute list_dir: err=%v res=%+v", err, res)
	}
	if len(mcp.calls) != 0 {
		t.Fatal("native call incorrectly routed to mcp source")
	}
}

func TestSurfaceExecuteRoutesMCP(t *testing.T) {
	roots := newTestRoots(t)
	fs := NewFSFamily(roots)
	mcp := &fakeMCPSource{}
	s := NewSurface(mcp, fs)

	res, err := s.Execute(context.Background(), Call{Name: "mcp__github__search", Args: json.RawMessage(`{"q":"x"}`)})
	if err != nil {
		t.Fatalf("Execute mcp tool: %v", err)
	}
	if res.Content != "mcp:mcp__github__search" {
		t.Fatalf("res = %+v", res)
	}
	if len(mcp.calls) != 1 || mcp.calls[0].Name != "mcp__github__search" {
		t.Fatalf("mcp.calls = %+v", mcp.calls)
	}
}

func TestSurfaceExecuteMCPPrefixNoSourceConfigured(t *testing.T) {
	roots := newTestRoots(t)
	fs := NewFSFamily(roots)
	s := NewSurface(nil, fs)

	res, err := s.Execute(context.Background(), Call{Name: "mcp__github__search", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError when no mcp source is configured")
	}
}

func TestSurfaceExecuteUnknownNameIsCleanError(t *testing.T) {
	roots := newTestRoots(t)
	fs := NewFSFamily(roots)
	s := NewSurface(nil, fs)

	res, err := s.Execute(context.Background(), Call{Name: "does_not_exist"})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for unknown tool name")
	}
}
