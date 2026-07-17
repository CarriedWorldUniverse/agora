package turnengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

func testRoots(t *testing.T) toolrunner.Roots {
	t.Helper()
	wd := t.TempDir()
	roots, err := toolrunner.NewRoots(wd)
	if err != nil {
		t.Fatalf("NewRoots: %v", err)
	}
	return roots
}

// TestSurfaceRunner_ReadFileSuccess is the unit-level (no bridle.Harness)
// proof that surfaceRunner actually executes a real fs tool through the
// Surface: seed a file, ask read_file to read it, get its content back as
// a valid JSON string (not raw bytes — see surfacerunner.go's doc on why
// json.Marshal, not a byte cast).
func TestSurfaceRunner_ReadFileSuccess(t *testing.T) {
	roots := testRoots(t)
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, "hello.txt"), []byte("hello from disk"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	surface := toolrunner.NewSurface(nil, toolrunner.NewFSFamily(roots), toolrunner.NewExecFamily(roots))
	runner := newSurfaceRunner(surface)

	raw, err := runner.Run(context.Background(), bridle.ToolCall{
		ID:   "1",
		Name: toolrunner.ToolReadFile,
		Args: json.RawMessage(`{"path":"hello.txt"}`),
	})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Run's return is not valid JSON: %v (raw=%s)", err, raw)
	}
	if got != "hello from disk" {
		t.Fatalf("got %q; want %q", got, "hello from disk")
	}
}

// TestSurfaceRunner_SourceContentNotHTMLEscaped pins the tool-result wire
// format decision (U-C2): reading real source code (which is full of < > &&)
// must reach the model faithfully. The runner's return must be VALID JSON
// (the claudesdk lane embeds it and marshals) that DECODES back to the exact
// bytes, and must NOT carry Go's default HTML-escapes (< etc.) in its
// on-the-wire form — because the direct-api/fake lane stringifies it verbatim.
func TestSurfaceRunner_SourceContentNotHTMLEscaped(t *testing.T) {
	roots := testRoots(t)
	src := "if a < b && c > d {\n\tfoo(\"x&y\")\n}\n"
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, "code.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	surface := toolrunner.NewSurface(nil, toolrunner.NewFSFamily(roots), toolrunner.NewExecFamily(roots))
	runner := newSurfaceRunner(surface)

	raw, err := runner.Run(context.Background(), bridle.ToolCall{
		ID:   "1",
		Name: toolrunner.ToolReadFile,
		Args: json.RawMessage(`{"path":"code.go"}`),
	})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	// Valid JSON that round-trips to the exact source.
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Run's return is not valid JSON: %v (raw=%s)", err, raw)
	}
	if got != src {
		t.Fatalf("decoded content = %q; want %q", got, src)
	}
	// On the wire, the JSON must contain the LITERAL metacharacters, NOT their
	// \u00xx HTML-escapes (the corruption default json.Marshal would introduce
	// on the verbatim-stringified direct-api lane).
	wire := string(raw)
	for _, esc := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(wire, esc) {
			t.Fatalf("wire form HTML-escaped (%s present): %s", esc, wire)
		}
	}
	for _, lit := range []string{"<", ">", "&"} {
		if !strings.Contains(wire, lit) {
			t.Fatalf("wire form missing literal metacharacter %q: %s", lit, wire)
		}
	}
}


// TestSurfaceRunner_UnknownTool: Surface.Execute's own "clean IsError
// Result, never panic" contract turns an unrecognized name into
// Result{IsError:true}, nil error — surfaceRunner maps THAT (a dispatch
// failure) onto a Go error return from Run, not a silent success (empty/
// zero-value json.RawMessage with a nil error, which would look to
// bridle's executeToolCall exactly like a successful, empty tool result).
func TestSurfaceRunner_UnknownTool(t *testing.T) {
	roots := testRoots(t)
	surface := toolrunner.NewSurface(nil, toolrunner.NewFSFamily(roots), toolrunner.NewExecFamily(roots))
	runner := newSurfaceRunner(surface)

	raw, err := runner.Run(context.Background(), bridle.ToolCall{
		ID:   "1",
		Name: "does_not_exist",
		Args: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("Run: expected a Go error for an unknown tool name, got nil")
	}
	if raw != nil {
		t.Fatalf("Run: expected nil result alongside the error, got %s", raw)
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("err = %q; want it to mention the unknown tool", err.Error())
	}
}

// TestSurfaceRunner_ToolLevelErrorIsGoErrorNotPanic: read_file on a
// protected .git path is a TOOL-level failure (Result.IsError, per fs.go's
// readFile: f.roots.IsProtected -> errorResult(ErrProtectedPath)), not a
// Surface-level Go error (toolrunner.Surface.Execute never returns a
// non-nil error for this — see errors.go's doc: "reserved for the harness
// itself misbehaving"). surfaceRunner still maps it to a Go error return
// from Run — per bridle's run.go executeToolCall, that is NOT a
// turn-aborting error (aborts only come from BeforeToolCall/AfterToolCall
// hooks, which never touch runner.Run's return at all): it becomes an
// "error: ..." tool_result the model sees and can react to. The
// harness-level assertion that this doesn't abort the turn lives in
// manager_test.go (TestManager_ToolLevelError_DoesNotAbortTurn).
func TestSurfaceRunner_ToolLevelErrorIsGoErrorNotPanic(t *testing.T) {
	roots := testRoots(t)
	if err := os.MkdirAll(filepath.Join(roots.WorkingDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, ".git", "config"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed .git/config: %v", err)
	}
	surface := toolrunner.NewSurface(nil, toolrunner.NewFSFamily(roots), toolrunner.NewExecFamily(roots))
	runner := newSurfaceRunner(surface)

	raw, err := runner.Run(context.Background(), bridle.ToolCall{
		ID:   "1",
		Name: toolrunner.ToolReadFile,
		Args: json.RawMessage(`{"path":".git/config"}`),
	})
	if err == nil {
		t.Fatal("Run: expected a Go error for a protected-path read, got nil")
	}
	if raw != nil {
		t.Fatalf("Run: expected nil result alongside the error, got %s", raw)
	}
	if !strings.Contains(err.Error(), "protected") {
		t.Fatalf("err = %q; want it to mention the protected path", err.Error())
	}
}

// TestToolDefsFromSpecs_StraightCopy asserts the 3-field
// contracts.ToolSpec -> bridle.ToolDef copy the brief calls for, including
// the empty-specs -> nil-Tools case (so an empty TurnRequest.Tools doesn't
// become a non-nil empty slice that changes wire/JSON shape downstream).
func TestToolDefsFromSpecs_StraightCopy(t *testing.T) {
	roots := testRoots(t)
	surface := toolrunner.NewSurface(nil, toolrunner.NewFSFamily(roots), toolrunner.NewExecFamily(roots))
	specs, err := surface.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs: %v", err)
	}
	defs := toolDefsFromSpecs(specs)
	if len(defs) != len(specs) {
		t.Fatalf("got %d defs; want %d", len(defs), len(specs))
	}
	for i, s := range specs {
		if defs[i].Name != s.Name || defs[i].Description != s.Description || string(defs[i].InputSchema) != string(s.InputSchema) {
			t.Fatalf("defs[%d] = %+v; want a straight copy of %+v", i, defs[i], s)
		}
	}

	if got := toolDefsFromSpecs(nil); got != nil {
		t.Fatalf("toolDefsFromSpecs(nil) = %#v; want nil", got)
	}
}
