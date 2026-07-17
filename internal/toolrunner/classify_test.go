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
	kind, _ := Classify(Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{Path: "/etc/passwd", OldString: "a", NewString: "b"})}, roots)
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
