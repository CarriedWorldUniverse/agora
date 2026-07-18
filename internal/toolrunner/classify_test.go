package toolrunner

import (
	"encoding/json"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestClassifyExec(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "rm -rf /tmp/x"})}, roots)
	if kind != contracts.KindExec {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindExec)
	}
	assertJSONExact(t, payload, `{"command":"rm -rf /tmp/x"}`)
}

func TestClassifyMCPTool(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: "mcp__github__search", Args: json.RawMessage(`{"q":"foo"}`)}, roots)
	if kind != contracts.KindMCPTool {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindMCPTool)
	}
	assertJSONExact(t, payload, `{"tool":"mcp__github__search","args":{"q":"foo"}}`)
}

// TestClassifyMCPToolNilArgs: DEVIATIONS.md §5's mcp_tool shape is exactly
// {tool, args} — a nil/omitted args must still marshal the key as
// "args":null, not drop it, since the TUI's decode side (and any other
// consumer) expects the field to always be present.
func TestClassifyMCPToolNilArgs(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: "mcp__github__search"}, roots)
	if kind != contracts.KindMCPTool {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindMCPTool)
	}
	assertJSONExact(t, payload, `{"tool":"mcp__github__search","args":null}`)
}

func TestClassifyWriteFileInsideRoots(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "a.txt", Content: "hello\nworld"})}, roots)
	if kind != contracts.KindPatch {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindPatch)
	}
	assertJSONExact(t, payload, `{"path":"a.txt","lines":[{"kind":1,"oldNo":0,"newNo":1,"text":"hello"},{"kind":1,"oldNo":0,"newNo":2,"text":"world"}]}`)
}

func TestClassifyWriteFileOutsideRoots(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "../escape.txt", Content: "x"})}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v, want non-empty EscalationPayload", payload)
	}
}

func TestClassifyWriteFileProtectedPath(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: ".git/config", Content: "x"})}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v, want non-empty EscalationPayload", payload)
	}
}

func TestClassifyEditFileDiff(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{
		Path:      "f.go",
		OldString: "func Foo() {\n\treturn 1\n}",
		NewString: "func Foo() {\n\treturn 2\n}",
	})}, roots)
	if kind != contracts.KindPatch {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindPatch)
	}
	assertJSONExact(t, payload, `{"path":"f.go","lines":[`+
		`{"kind":0,"oldNo":1,"newNo":1,"text":"func Foo() {"},`+
		`{"kind":2,"oldNo":2,"newNo":0,"text":"\treturn 1"},`+
		`{"kind":1,"oldNo":0,"newNo":2,"text":"\treturn 2"},`+
		`{"kind":0,"oldNo":3,"newNo":3,"text":"}"}]}`)
}

func TestClassifyEditFileOutsideRoots(t *testing.T) {
	roots := newTestRoots(t)
	kind, _ := Classify(Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{Path: absOutsideRoots(), OldString: "a", NewString: "b"})}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
}

func TestClassifyMalformedArgsIsEscalation(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolRunCommand, Args: json.RawMessage(`not json`)}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestClassifyUnknownToolIsEscalation(t *testing.T) {
	roots := newTestRoots(t)
	kind, _ := Classify(Call{Name: "nonexistent_tool"}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
}

// TestClassifyReadTools: read_file/list_dir/glob/grep all classify as
// KindRead (NEX-782), carrying the call's path/pattern in ReadPayload.
func TestClassifyReadTools(t *testing.T) {
	roots := newTestRoots(t)

	kind, payload := Classify(Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "a.txt"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("read_file kind = %v, want %v", kind, contracts.KindRead)
	}
	if p, ok := payload.(ReadPayload); !ok || p.Detail != "a.txt" {
		t.Fatalf("read_file payload = %#v, want ReadPayload{Detail: \"a.txt\"}", payload)
	}

	kind, payload = Classify(Call{Name: ToolListDir, Args: mustArgs(t, listDirArgs{Path: "sub"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("list_dir kind = %v, want %v", kind, contracts.KindRead)
	}
	if p, ok := payload.(ReadPayload); !ok || p.Detail != "sub" {
		t.Fatalf("list_dir payload = %#v, want ReadPayload{Detail: \"sub\"}", payload)
	}

	kind, payload = Classify(Call{Name: ToolGlob, Args: mustArgs(t, globArgs{Pattern: "**/*.go"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("glob kind = %v, want %v", kind, contracts.KindRead)
	}
	if p, ok := payload.(ReadPayload); !ok || p.Detail != "**/*.go" {
		t.Fatalf("glob payload = %#v, want ReadPayload{Detail: \"**/*.go\"}", payload)
	}

	kind, payload = Classify(Call{Name: ToolGrep, Args: mustArgs(t, grepArgs{Pattern: "TODO"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("grep kind = %v, want %v", kind, contracts.KindRead)
	}
	if p, ok := payload.(ReadPayload); !ok || p.Detail != "TODO" {
		t.Fatalf("grep payload = %#v, want ReadPayload{Detail: \"TODO\"}", payload)
	}
}

// TestClassifyReadFileMalformedArgsIsEscalation: read tools follow the
// same malformed-Args-> KindEscalation fail-closed convention as every
// other case.
func TestClassifyReadFileMalformedArgsIsEscalation(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolReadFile, Args: json.RawMessage(`not json`)}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v", payload)
	}
}

// assertJSONExact marshals payload and compares it byte-for-byte against
// want (both parsed and re-marshaled through the same encoder so key
// ordering from struct field order is deterministic) — the DEVIATIONS.md
// §5 shapes are load-bearing for the TUI modal, so this checks the exact
// wire bytes a producer would emit, not just semantic equivalence.
func assertJSONExact(t *testing.T, payload any, want string) {
	t.Helper()
	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if string(got) != want {
		t.Fatalf("payload JSON =\n%s\nwant\n%s", got, want)
	}
}
